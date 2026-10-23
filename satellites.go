package main

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/kaitai-io/kaitai_struct_go_runtime/kaitai"

	"github.com/supremeinferno/satnogs-pipeline/internal/decoders/catsat"
	"github.com/supremeinferno/satnogs-pipeline/internal/decoders/sharjahsat1"
	"github.com/supremeinferno/satnogs-pipeline/internal/decoders/snuglite"
)

// ErrImplausible means the frame parsed but its battery voltage is outside
// what the satellite's battery can have, so it was probably damaged or is a
// different kind of packet.
var ErrImplausible = errors.New("battery voltage outside the plausible range")

// BatteryReading is the state of one battery in one beacon. The optional
// values are nil when the beacon does not carry them, or when the satellite's
// sign or unit has not been checked against real data.
type BatteryReading struct {
	Battery   string // "a" and "b" for UWE-4, "main" for a single pack
	VoltageMV int
	CurrentMA *int // positive charging
	TempC     *int
	ChargePct *int
}

// Decoded is everything a beacon says about the batteries.
type Decoded struct {
	Batteries []BatteryReading
}

// satellite is what the pipeline knows about one satellite.
type satellite struct {
	NORAD int
	Name  string
	// Decode turns a frame as received into battery readings. It returns
	// ErrNotBeacon for frames without battery data, ErrBadChecksum or
	// ErrImplausible for damaged ones.
	Decode func(frame []byte) (Decoded, error)
	// LowMV, if not 0, is the voltage below which a battery is in trouble in
	// any light. Satellites without it are only watched against their own
	// baseline.
	LowMV int
	// StaleAfter is how long without a beacon is worth an alert. Satellites are
	// heard at very different rates, so it is set from the largest gap seen in
	// the stored beacons (23 Oct 2026): UWE-4 7.7 h over 5 days, SNUGLITE 7.7 h
	// over 1.4 days, CatSat 9.0 h over 0.9 days, Sharjahsat-1 45.2 h over 9 days
	// (few of its packets carry a battery). At least twice the largest gap, and
	// 24 h where there are under three days of history. Revisit as data grows.
	StaleAfter time.Duration
}

// satellites is the list the pipeline watches. Adding one means adding its
// generated parser (scripts/kaitai/generate.sh), the name of the battery
// fields in it, and checking them against real frames.
var satellites = []satellite{
	{NORAD: uwe4NORAD, Name: "UWE-4", Decode: decodeUWE4Readings, LowMV: 3500, StaleAfter: 12 * time.Hour},
	{
		NORAD: 60246, Name: "CatSat", StaleAfter: 24 * time.Hour,
		// Gomspace P60 power system: millivolts and milliamps. A four-cell pack.
		// The current's sign was checked against the lighting (23 Oct 2026, 37
		// beacons over 1 day): in the shadow 20 of 20 negative, so positive is
		// charging.
		Decode: kaitaiDecoder(func(s *kaitai.Stream) (any, error) { var v catsat.Catsat; return &v, v.Read(s, nil, &v) },
			batterySpec{Voltage: "P60BattV", VoltageScale: 1, MinMV: 8000, MaxMV: 20000, Current: "P60BattC", CurrentScale: 1}),
	},
	{
		NORAD: 43784, Name: "SNUGLITE", StaleAfter: 24 * time.Hour,
		// The definition gives no unit; as millivolts the readings (about 8.3 V)
		// are a two-cell pack. The baseline rule does not depend on the unit.
		Decode: kaitaiDecoder(func(s *kaitai.Stream) (any, error) { var v snuglite.Snuglite; return &v, v.Read(s, nil, &v) },
			batterySpec{Voltage: "BatteryVoltage", VoltageScale: 1, MinMV: 4000, MaxMV: 12000}),
	},
	{
		NORAD: 55104, Name: "Sharjahsat-1", StaleAfter: 96 * time.Hour,
		// "x*0.008993 [V]" in the definition: about 7.9 V, a two-cell pack.
		// Current "x*14.662757 [mA]", but positive is discharging: in the
		// shadow 9 of 9 beacons were positive, in sunlight 8 of 9 negative, with
		// the voltage higher when it is negative (23 Oct 2026, 18 beacons over 8
		// days). So the scale is negated.
		Decode: kaitaiDecoder(func(s *kaitai.Stream) (any, error) { var v sharjahsat1.Sharjahsat1; return &v, v.Read(s, nil, &v) },
			batterySpec{Voltage: "Vbat", VoltageScale: 8.993, MinMV: 4000, MaxMV: 12000, Current: "Ibat", CurrentScale: -14.662757}),
	},
}

func lookupSatellite(norad int) (satellite, bool) {
	for _, s := range satellites {
		if s.NORAD == norad {
			return s, true
		}
	}
	return satellite{}, false
}

// decodeUWE4Readings adapts the UWE-4 decoder, which checks the frame's
// checksum itself, to the general form.
func decodeUWE4Readings(frame []byte) (Decoded, error) {
	u, err := DecodeUWE4(frame)
	if err != nil {
		return Decoded{}, err
	}
	reading := func(name string, b Battery) BatteryReading {
		ma, temp, pct := b.CurrentMA, b.TempC, b.ChargePct
		return BatteryReading{Battery: name, VoltageMV: b.VoltageMV, CurrentMA: &ma, TempC: &temp, ChargePct: &pct}
	}
	return Decoded{Batteries: []BatteryReading{reading("a", u.BattA), reading("b", u.BattB)}}, nil
}

// batterySpec names the fields of a generated parser that hold the battery.
type batterySpec struct {
	Voltage      string  // field name, searched for anywhere in the parsed frame
	VoltageScale float64 // millivolts per unit of the field
	MinMV, MaxMV int     // plausible range, in place of a checksum
	Current      string  // optional, only once its sign has been checked against the lighting
	CurrentScale float64
}

// kaitaiDecoder builds a Decode function from a generated parser and a spec.
// Whatever the parser does with an odd frame (an error or a panic), the result
// is ErrNotBeacon, never a crash.
func kaitaiDecoder(parse func(*kaitai.Stream) (any, error), spec batterySpec) func([]byte) (Decoded, error) {
	return func(frame []byte) (d Decoded, err error) {
		defer func() {
			if r := recover(); r != nil {
				d, err = Decoded{}, fmt.Errorf("%w: parser panic: %v", ErrNotBeacon, r)
			}
		}()
		parsed, perr := parse(kaitai.NewStream(bytes.NewReader(frame)))
		if perr != nil {
			return Decoded{}, fmt.Errorf("%w: %v", ErrNotBeacon, perr)
		}
		raw, ok := findNumber(reflect.ValueOf(parsed), spec.Voltage, 0)
		if !ok {
			return Decoded{}, fmt.Errorf("%w: no %s in this packet", ErrNotBeacon, spec.Voltage)
		}
		mv := int(math.Round(raw * spec.VoltageScale))
		if mv < spec.MinMV || mv > spec.MaxMV {
			return Decoded{}, fmt.Errorf("%w: %d mV", ErrImplausible, mv)
		}
		r := BatteryReading{Battery: "main", VoltageMV: mv}
		if spec.Current != "" {
			if c, ok := findNumber(reflect.ValueOf(parsed), spec.Current, 0); ok {
				ma := int(math.Round(c * spec.CurrentScale))
				r.CurrentMA = &ma
			}
		}
		return Decoded{Batteries: []BatteryReading{r}}, nil
	}
}

// findNumber returns the first numeric field called name, searching the
// parsed value depth first. Generated parsers nest a frame's packet types
// differently for every satellite; the battery fields have distinct names.
func findNumber(v reflect.Value, name string, depth int) (float64, bool) {
	if depth > 16 {
		return 0, false
	}
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return 0, false
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() || strings.HasPrefix(f.Name, "_") {
				continue
			}
			if f.Name == name {
				switch k := v.Field(i); k.Kind() {
				case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					return float64(k.Uint()), true
				case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					return float64(k.Int()), true
				case reflect.Float32, reflect.Float64:
					return k.Float(), true
				}
			}
			if n, ok := findNumber(v.Field(i), name, depth+1); ok {
				return n, true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len() && i < 8; i++ {
			if n, ok := findNumber(v.Index(i), name, depth+1); ok {
				return n, true
			}
		}
	}
	return 0, false
}
