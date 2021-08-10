package sgp30

// datasheet: https://www.mouser.com/datasheet/2/682/Sensirion_Gas_Sensors_SGP30_Datasheet_EN-1148053.pdf
// integration guide: https://cdn.sos.sk/productdata/b2/66/3af4ba1f/sgp30.pdf

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/sigurn/crc8"
)

const (
	GetBaseline          uint16 = 0x2015
	GetFeatureSetVersion uint16 = 0x202f
	GetSerialID          uint16 = 0x3682
	InitAirQuality       uint16 = 0x2003
	MeasureAirQuality    uint16 = 0x2008
	SetBaseline          uint16 = 0x201e
	SetHumidity          uint16 = 0x2061
	MeasureRawSignals    uint16 = 0x2050
	MeasureTest          uint16 = 0x2032
)

// ValidFeaturesets are the supported featuresets of the SGP30.
var ValidFeaturesets = []uint16{0x0020, 0x0022}

// Bus is the interface for I2C communication.
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

// SGP30Sensor provides methods to interact with the sensor.
type SGP30Sensor struct {
	bus             Bus
	lastMeasurement *Measurement
	stopChan        chan bool
	initialBaseline *Measurement
	initialHumidity *uint16
}

// Measurement encapsulates air quality readings from the sensor.
type Measurement struct {
	ECO2 uint16
	TVOC uint16
}

// New creates a Sensor and validates the connection.
func New(i2cbus Bus) *SGP30Sensor {
	return &SGP30Sensor{
		bus:      i2cbus,
		stopChan: make(chan bool, 1),
	}
}

// WithBaseline restores the baseline to a previous known value in the init proccess.
// Calling this after `Start()` will have no effect until the next time `Start()` is called.
// Call `GetBseline()` every hour after an initial 12 hour calibration phase to obtain the value.
// > After a power-up or soft reset, the baseline of the baseline correction algorithm can be restored by sending first an
// > “Init_air_quality” command followed by a “Set_baseline” command
func (s *SGP30Sensor) WithBaseline(bl Measurement) *SGP30Sensor {
	s.initialBaseline = &bl
	return s
}

// WithHumidity sets the humidity compensation value in the init proccess.
// Calling this after `Start()` will have no effect until the next time `Start()` is called.
// > Call set_humidity to a value greater than 0 and smaller than 256000 mg/m3 to enable the humidity compensation feature, or write 0 to disable it.
// Changes in humidity can be provided using the `.SetHumidity()` method during sampling.
func (s *SGP30Sensor) WithHumidity(hm uint16) *SGP30Sensor {
	s.initialHumidity = &hm
	return s
}

// Start intialises the sensor according to the manufacturer documentation,
// including reseting the baseline compensation algorithm and polling at the
// recommended 1 second interval.
//
// For the first 15s the sensor is in an initialization phase during which
// measurements return fixed values of 400 ppm CO2eq and 0 ppb TVOC.
//
// Use `.New().WithHumidity()` and `.WithBaseline()` to set compensation values immediately after init.
//
// > If no stored baseline is available after initializing the baseline algorithm, the sensor has to run for 12 hours until the baseline can be stored.
// > This will ensure an optimal behavior for preceding startups. Reading out the baseline prior should be avoided unless a valid baseline is restored first.
// > Once the baseline is properly initialized or restored, the current baseline value should be stored approximately once per hour.
// > While the sensor is off, baseline values are valid for a maximum of seven days.
func (s *SGP30Sensor) Start() error {
	// see if we can get the serial to validate connection and device
	if _, err := s.GetSerial(); err != nil {
		return fmt.Errorf("sgp30: failed to get serial id")
	}

	// check featureset to validate connection and device
	if featureSet, err := s.GetFeatureSet(); err == nil {
		found := false
		for _, vf := range ValidFeaturesets {
			if featureSet == vf {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("sgp30: featureset mismatch %x", featureSet)
		}
	} else {
		return fmt.Errorf("sgp30: failed to get feature set")
	}

	// reset all compensation and start measurement
	if err := s.InitAirQuality(); err != nil {
		return fmt.Errorf("sgp30: %w", err)
	}

	// immediately after init we can set a baseline, if one was provided
	if s.initialBaseline != nil {
		if err := s.SetBaseline(s.initialBaseline.ECO2, s.initialBaseline.TVOC); err != nil {
			return fmt.Errorf("sgp30: %w", err)
		}
	}

	// immediately after init we can set the humidity compensator, if a value was provided
	if s.initialHumidity != nil {
		if err := s.SetHumidity(*s.initialHumidity); err != nil {
			return fmt.Errorf("sgp30: %w", err)
		}
	}

	// > After the “Init_air_quality” command, a “Measure_air_quality” command has to be sent in regular
	// > intervals of 1s to ensure proper operation of the dynamic baseline compensation algorithm.
	go s.sample()

	return nil
}

// Stop ends sampling.
// Restarting sampling requires re-initialisation of the baseline compensation, so you probably don't want to do this.
func (s *SGP30Sensor) Stop() {
	s.stopChan <- true
}

// sample polls the sensor each second
func (s *SGP30Sensor) sample() {
	timer := time.NewTicker(1 * time.Second)

	for {
		select {
		case <-timer.C:
			if m, err := s.measureDirect(); err == nil {
				s.lastMeasurement = &m
			}

		case <-s.stopChan:
			timer.Stop()
			s.lastMeasurement = nil

		}
	}
}

// Measure returns air quality measurements, using the most recently sampled value if available.
// Otherwise, directly measures from the sensor.
func (s *SGP30Sensor) Measure() (Measurement, error) {
	if s.lastMeasurement != nil {
		return *s.lastMeasurement, nil
	}

	return s.measureDirect()
}

// measureDirect returns air quality measurements directly from the sensor.
func (s *SGP30Sensor) measureDirect() (Measurement, error) {
	vals, err := s.doCommand(MeasureAirQuality)
	if err != nil {
		return Measurement{}, fmt.Errorf("sgp30: %w", err)
	}

	return Measurement{vals[0], vals[1]}, nil
}

// GetBaseline returns a measurement representing the baseline readings.
// > For best performance and faster startup times, the current baseline needs to be persistently stored before power off and set again accordingly after boot up.
// > Approximately in the first 60min of operation after init the call will fail unless a previous baseline was restored.
func (s *SGP30Sensor) GetBaseline() (Measurement, error) {
	vals, err := s.doCommand(GetBaseline)
	if err != nil {
		return Measurement{}, fmt.Errorf("sgp30: %w", err)
	}

	return Measurement{vals[0], vals[1]}, nil
}

// SetBaseline sets the baseline values of the sensor
// > The baseline value must be exactly as returned by get_baseline and should only be set if it’s less than one week old.
func (s *SGP30Sensor) SetBaseline(ECO2 uint16, TVOC uint16) error {
	buffer := make([]byte, 2)
	binary.BigEndian.PutUint16(buffer, SetBaseline)

	buffer = append(buffer, packWordCrc(ECO2)...)
	buffer = append(buffer, packWordCrc(TVOC)...)

	_, err := s.do(buffer, 10, 0)
	if err != nil {
		return fmt.Errorf("sgp30: %w", err)
	}

	return nil
}

// GetSerial returns the ID value directly from the sensor.
func (s *SGP30Sensor) GetSerial() (uint64, error) {
	vals, err := s.doCommand(GetSerialID)
	if err != nil {
		return 0, fmt.Errorf("sgp30: failed to read serial: %w", err)
	}

	return combineWords(vals), nil
}

// GetFeatureSet returns the featureset value directly from the sensor.
func (s *SGP30Sensor) GetFeatureSet() (uint16, error) {
	vals, err := s.doCommand(GetFeatureSetVersion)
	if err != nil {
		return 0, fmt.Errorf("sgp30: failed to get feature set: %w", err)
	}

	return vals[0], nil
}

// SetHumidity configures the humidity compensation. Set to zero to disable.
// > Call set_humidity to a value greater than 0 and smaller than 256000 mg/m3 to enable the humidity compensation feature, or write 0 to disable it.
// > The absolute humidity in g/m3 can be retrieved by measuring the relative humidity and temperature and converting the value to absolute humidity
// > With AH in g/m3, RH in 0-100%, and t in °C
// > Note: the value in g/m3 has to be multiplied by 1000 to convert to mg/m3 and any remaining decimal places have to be rounded and removed since the interface does not support floating point numbers.
// > Note: The humidity compensation is disabled by setting the value to 0.
func (s *SGP30Sensor) SetHumidity(absHumidity uint16) error {
	buffer := make([]byte, 2)
	binary.BigEndian.PutUint16(buffer, SetHumidity)

	buffer = append(buffer, packWordCrc(absHumidity)...)

	_, err := s.do(buffer, 10, 0)
	if err != nil {
		return fmt.Errorf("sgp30: %w", err)
	}

	return nil
}

// InitAirQuality prepares the sensor for taking measurements.
// > Call init to initialize or re-initialize the indoor air quality algorithm.
// > Call init to reset all SGP baselines. The initialization takes up to around 15 seconds, during which measurements will not change.
func (s *SGP30Sensor) InitAirQuality() (err error) {
	_, err = s.doCommand(InitAirQuality)
	return
}

func (s *SGP30Sensor) doCommand(command uint16) (result []uint16, err error) {
	if s.bus == nil {
		return nil, fmt.Errorf("sgp30: i2c not connected")
	}

	// pg 9 table 10 of datasheet
	replySizeWords := 0
	delayMs := 0
	switch command {
	case GetBaseline:
		replySizeWords = 2
		delayMs = 10
	case GetFeatureSetVersion:
		replySizeWords = 1
		delayMs = 2
	case GetSerialID:
		delayMs = 1
	case InitAirQuality:
		delayMs = 10
	case MeasureAirQuality:
		replySizeWords = 2
		delayMs = 12
	case SetBaseline:
		delayMs = 10
	case SetHumidity:
		delayMs = 10
	case MeasureRawSignals:
		replySizeWords = 2
		delayMs = 25
	case MeasureTest:
		replySizeWords = 1
		delayMs = 220
	}

	buffer := make([]byte, 2)
	binary.BigEndian.PutUint16(buffer, command)

	return s.do(buffer, delayMs, replySizeWords)
}

func (s *SGP30Sensor) do(buffer []byte, delayMs int, replySizeWords int) (result []uint16, err error) {
	_, err = s.bus.WriteBytes(buffer)
	if err != nil {
		return result, fmt.Errorf("sgp30: failed writing command %s: %w", hex.Dump(buffer), err.Error())
	}

	time.Sleep(time.Duration(delayMs) * time.Millisecond)

	if replySizeWords == 0 {
		return result, nil
	}

	crcResult := make([]byte, replySizeWords*3)
	_, err = s.bus.ReadBytes(crcResult)
	if err != nil {
		return result, fmt.Errorf("sgp30: failed read: %w", err)
	}

	result = make([]uint16, replySizeWords)

	for i := 0; i < replySizeWords; i++ {
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
