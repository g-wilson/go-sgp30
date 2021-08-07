package sgp30

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/sigurn/crc8"
)

const (
	InitAirQuality       uint16 = 0x2003
	MeasureAirQuality    uint16 = 0x2008
	GetBaseline          uint16 = 0x2015
	SetBaseline          uint16 = 0x201e
	SetHumidity          uint16 = 0x2061
	MeasureTest          uint16 = 0x2032
	GetFeatureSetVersion uint16 = 0x202f
	MeasureRawSignals    uint16 = 0x2050
	GetSerialID          uint16 = 0x3682
)

var ValidFeaturesets = []uint16{0x0020, 0x0022}

type Bus interface {
	ReadBytes(buf []byte) (int, error)
	WriteBytes(buf []byte) (int, error)
}

var crcTable = crc8.MakeTable(crc8.Params{
	Poly:   0x31,
	Init:   0xFF,
	RefIn:  false,
	RefOut: false,
	XorOut: 0x00,
	Check:  0xF7,
})

type SGP30Sensor struct {
	bus                Bus
	measurementChannel chan Measurement
	errorChannel       chan error
}

type Measurement struct {
	ECO2 uint16
	TVOC uint16
	Test bool
}

func New(i2cbus Bus) (*SGP30Sensor, error) {
	s := &SGP30Sensor{
		bus:                i2cbus,
		measurementChannel: make(chan Measurement, 1),
		errorChannel:       make(chan error, 1),
	}

	if featureSet, err := s.GetFeatureSet(); err == nil {
		found := false
		for _, vf := range ValidFeaturesets {
			if featureSet == vf {
				found = true
			}
		}
		if !found {
			return s, fmt.Errorf("sgp30: featureset mismatch %x", featureSet)
		}
	} else {
		return s, fmt.Errorf("sgp30: failed to get feature set")
	}

	return s, nil
}

// Listen initialises the dynamic baseline measurement
// and then continuously polls for new measurements
func (s *SGP30Sensor) Listen() (<-chan Measurement, <-chan error) {
	go (func() {
		measureInterval := time.NewTicker(time.Second)
		baselineInterval := time.NewTicker(10 * time.Second)

		// calibration phase
		testSamples := 0
		if _, err := s.readWordsUint(InitAirQuality, 0); err != nil {
			s.errorChannel <- fmt.Errorf("sgp30: %w", err)
			return
		}
		for range measureInterval.C {
			m, err := s.Measure()
			if err != nil {
				s.errorChannel <- fmt.Errorf("sgp30: %w", err)
				return
			}

			testSamples += 1
			m.Test = true
			s.measurementChannel <- m

			if m.ECO2 != 400 || m.TVOC != 0 {
				m, err := s.GetBaseline()
				if err != nil {
					s.errorChannel <- err
					return
				}
				err = s.SetBaseline(m.ECO2, m.TVOC)
				if err != nil {
					s.errorChannel <- err
					return
				}
				break
			}
			if testSamples > 19 {
				s.errorChannel <- errors.New("sgp30: baseline not established")
				return
			}
		}

		// measurement phase
		for {
			select {
			case <-measureInterval.C:
				m, err := s.Measure()
				if err != nil {
					s.errorChannel <- err
				} else {
					s.measurementChannel <- m
				}

			case <-baselineInterval.C:
				m, err := s.GetBaseline()
				if err != nil {
					s.errorChannel <- err
					continue
				}
				err = s.SetBaseline(m.ECO2, m.TVOC)
				if err != nil {
					s.errorChannel <- err
				}
			}
		}
	})()

	return s.measurementChannel, s.errorChannel
}

func (s *SGP30Sensor) Measure() (m Measurement, err error) {
	vals, err := s.readWordsUint(MeasureAirQuality, 2)
	if err != nil {
		return
	}

	return Measurement{vals[0], vals[1], false}, nil
}

func (s *SGP30Sensor) GetBaseline() (m Measurement, err error) {
	vals, err := s.readWordsUint(GetBaseline, 2)
	if err != nil {
		return
	}

	return Measurement{vals[0], vals[1], false}, nil
}

func (s *SGP30Sensor) SetBaseline(ECO2 uint16, TVOC uint16) error {
	buffer := make([]byte, 2)
	binary.BigEndian.PutUint16(buffer, SetBaseline)

	buffer = append(buffer, packWordCrc(ECO2)...)
	buffer = append(buffer, packWordCrc(TVOC)...)

	_, err := s.readWords(buffer, 0)

	return err
}

func (s *SGP30Sensor) GetSerial() (uint64, error) {
	vals, err := s.readWordsUint(GetSerialID, 3)
	if err != nil {
		return 0, fmt.Errorf("failed to read serial: %s", err)
	}

	return combineWords(vals), nil
}

func (s *SGP30Sensor) GetFeatureSet() (uint16, error) {
	vals, err := s.readWordsUint(GetFeatureSetVersion, 1)
	if err != nil {
		return 0, fmt.Errorf("failed to get feature set: %s", err)
	}

	return vals[0], nil
}

func (s *SGP30Sensor) readWordsUint(command uint16, replySize int) (result []uint16, err error) {
	buffer := make([]byte, 2)
	binary.BigEndian.PutUint16(buffer, command)

	return s.readWords(buffer, replySize)
}

func (s *SGP30Sensor) readWords(command []byte, replySize int) (result []uint16, err error) {
	if s.bus == nil {
		return nil, fmt.Errorf("sgp30: i2c not connected")
	}

	_, err = s.bus.WriteBytes(command)
	if err != nil {
		return result, fmt.Errorf("sgp30: failed writing command %s: %s", hex.Dump(command), err.Error())
	}

	time.Sleep(10 * time.Millisecond)
	if replySize == 0 {
		return result, nil
	}

	crcResult := make([]byte, replySize*(3))
	_, err = s.bus.ReadBytes(crcResult)
	if err != nil {
		return result, fmt.Errorf("sgp30: failed read: %s", err)
	}

	result = make([]uint16, replySize)

	for i := 0; i < replySize; i++ {
		word := []byte{crcResult[3*i], crcResult[3*i+1]}
		crc := crcResult[3*i+2]

		generatedCrc := crc8.Checksum(word, crcTable)
		if generatedCrc != crc {
			return nil, fmt.Errorf("sgp30: crc mismatch %+v, %+v", crc, generatedCrc)
		}

		result[i] = binary.BigEndian.Uint16([]byte{word[0], word[1]})
	}

	return result, nil
}

func packWordCrc(word uint16) []byte {
	buffer := make([]byte, 2)
	binary.BigEndian.PutUint16(buffer, word)
	buffer = append(buffer, crc8.Checksum(buffer, crcTable))

	return buffer
}

func combineWords(words []uint16) uint64 {
	combined := make([]byte, 8)

	for i := range words {
		buf := make([]byte, 2)
		binary.BigEndian.PutUint16(buf, words[len(words)-1-i])
		combined[7-2*i] = buf[1]
		combined[7-(2*i+1)] = buf[0]
	}

	return binary.BigEndian.Uint64(combined)
}
