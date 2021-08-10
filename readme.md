# go-sgp30

A Go library to read eCO2 (Equivalent Carbon Dioxide) and TVOC (Total Volatile Organic Compounds) sensor data from a SGP30 Sensirion Gas Platform digital sensor.

A huge amount of this code is based on [ataboo's library](https://github.com/ataboo/sgp30go) however this one is compatible with the [go-i2c](https://github.com/d2r2/go-i2c) library, simplifies a few things, and adds support for setting the humidity compensation.

Implementation checked against documentation here https://cdn.sos.sk/productdata/b2/66/3af4ba1f/sgp30.pdf

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

	sensor := sgp30.New(bus)
		.WithHumidity(0)

	err = sensor.Start()
	if err != nil {
		log.Fatal(err)
	}

	for {
		select {
		case <-time.Tick(time.Second):
			m, err := sensor.Measure()
			if err != nil {
				log.Println(err)
			} else {
				log.Printf("sgp30 measurement: eco2: %v tvoc: %v\n", m.ECO2, m.TVOC)
			}

		case <-time.Tick(time.Hour):
			m, err := sensor.GetBaseline()
			if err != nil {
				log.Println(err)
			} else {
				log.Printf("sgp30 baseline: eco2: %v tvoc: %v\n", m.ECO2, m.TVOC)
			}

		}
}
```
