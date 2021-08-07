# go-sgp30

A Go library to read eCO2 (Equivalent Carbon Dioxide) and TVOC (Total Volatile Organic Compounds) sensor data from a SGP30 Sensirion Gas Platform digital sensor.

A huge amount of this code is based on [ataboo's library](https://github.com/ataboo/sgp30go) however this one is compatible with the [go-i2c](https://github.com/d2r2/go-i2c) library, and has a convenient `Listen` method which correctly sets up and takes care of the baseline compensation feature of the sensor before polling at the recommended frequency of 1 Hz.

This was written against the I2C interface of a Raspberry Pi using the `github.com/d2r2/go-i2c` library although it is not a dependency - you just have to meet the following interface:

```go
type Bus interface {
	ReadBytes(buf []byte) (int, error)
	WriteBytes(buf []byte) (int, error)
}
```

### Example usage

```go
package main

import (
	"log"

	"github.com/g-wilson/go-sgp30"

	"github.com/d2r2/go-i2c"
	"github.com/kr/pretty"
)

const sensorAddress = 0x10

func main() {
	bus, err := i2c.NewI2C(sensorAddress, 1)
	if err != nil {
		log.Fatal(err)
	}
	defer bus.Close()

	sensor, err := sgp30.New(sgp30bus)
	if err != nil {
		log.Fatal(err)
	}

	// polls once per second for measurements
	measurements, errors := sensor.Listen()
	for {
		select {
		case m := <-sgp30res:
			if m.Test {
				log.Println("calibrating...")
			} else {
				log.Printf("eco2: %v tvoc: %v\n", m.ECO2, m.TVOC)
			}
		case err := <-sgp30errors:
			log.Println(err)
		}
	}
}
```
