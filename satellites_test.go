package main

import (
	"bufio"
	"encoding/hex"
	"errors"
	"github.com/kaitai-io/kaitai_struct_go_runtime/kaitai"
	"math/rand"
	"os"
	"slices"
	"strings"
	"testing"
)

// framesOf reads testdata/<name>_frames.txt.
func framesOf(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open("testdata/" + name + "_frames.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		_, h, _ := strings.Cut(line, " ")
		b, err := hex.DecodeString(h)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

// The voltages are what the generated parsers read from these real frames when
// they were first tried: with the satellite's own definition of the unit, a
// plausible lithium pack in each case. They guard against a regression; the
// plausibility is the check against the real thing.
func TestSatelliteDecodersOnRealFrames(t *testing.T) {
	tests := []struct {
		name  string
		norad int
		want  []int // voltages that must be among the decoded ones, in mV
		// currents that must be among the decoded ones, in mA, positive
		// charging; nil for a satellite whose current is not used
		wantMA []int
	}{
		{"catsat", 60246, []int{16303, 15524}, []int{170, -340}},
		{"snuglite", 43784, []int{8315, 8277}, nil},
		{"sharjahsat1", 55104, []int{7905}, []int{103}}, // raw 879 x 8.993 mV; raw -7 x -14.66 mA, in sunlight
		{"grbbeta", 60237, []int{16693}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sat, ok := lookupSatellite(tt.norad)
			if !ok {
				t.Fatalf("satellite %d is not in the registry", tt.norad)
			}
			var volts, amps []int
			skipped := 0
			for _, f := range framesOf(t, tt.name) {
				d, err := sat.Decode(f)
				switch {
				case err == nil:
					if len(d.Batteries) != 1 || d.Batteries[0].Battery != "main" {
						t.Fatalf("batteries %+v", d.Batteries)
					}
					volts = append(volts, d.Batteries[0].VoltageMV)
					if c := d.Batteries[0].CurrentMA; c != nil {
						amps = append(amps, *c)
					}
				case errors.Is(err, ErrNotBeacon):
					skipped++
				default:
					t.Fatalf("real frame gave %v", err)
				}
			}
			if len(volts) == 0 {
				t.Fatalf("no frame decoded (%d skipped)", skipped)
			}
			for _, w := range tt.want {
				if !slices.Contains(volts, w) {
					t.Errorf("voltages %v do not include %d", volts, w)
				}
			}
			for _, w := range tt.wantMA {
				if !slices.Contains(amps, w) {
					t.Errorf("currents %v do not include %d", amps, w)
				}
			}
			if tt.wantMA == nil && len(amps) > 0 {
				t.Errorf("currents %v from a satellite whose sign is not checked", amps)
			}
			t.Logf("%d frames decoded, %d are other packet types", len(volts), skipped)
		})
	}
}

// Whatever arrives from the radio, the decoders return an error, never panic.
func TestSatelliteDecodersNeverPanic(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, sat := range satellites {
		var real [][]byte
		for _, name := range []string{"catsat", "snuglite", "sharjahsat1", "grbbeta", "uwe4"} {
			if name == "uwe4" {
				for _, f := range realFrames(t) {
					real = append(real, f)
				}
				continue
			}
			real = append(real, framesOf(t, name)...)
		}
		for _, f := range real {
			// every prefix, as a frame cut short
			for n := 0; n <= len(f); n += 1 + len(f)/40 {
				sat.Decode(f[:n])
			}
			// every frame with one byte changed
			for i := 0; i < 30; i++ {
				c := slices.Clone(f)
				if len(c) > 0 {
					c[rng.Intn(len(c))] = byte(rng.Intn(256))
				}
				sat.Decode(c)
			}
		}
		for i := 0; i < 200; i++ {
			junk := make([]byte, rng.Intn(300))
			rng.Read(junk)
			sat.Decode(junk)
		}
	}
}

func TestRegistry(t *testing.T) {
	seen := map[int]bool{}
	for _, s := range satellites {
		if s.NORAD == 0 || s.Name == "" || s.Decode == nil || s.StaleAfter <= 0 {
			t.Errorf("incomplete entry %+v", s.Name)
		}
		if seen[s.NORAD] {
			t.Errorf("%d twice", s.NORAD)
		}
		seen[s.NORAD] = true
	}
	if _, ok := lookupSatellite(1); ok {
		t.Error("an unknown satellite must not be found")
	}
}

func TestUWE4ThroughTheRegistry(t *testing.T) {
	sat, _ := lookupSatellite(uwe4NORAD)
	d, err := sat.Decode(realFrames(t)["2026-10-22T11:00:08Z"])
	if err != nil || len(d.Batteries) != 2 {
		t.Fatalf("%v, %+v", err, d)
	}
	a := d.Batteries[0]
	if a.Battery != "a" || a.VoltageMV != 4108 || a.CurrentMA == nil || *a.CurrentMA != 118 {
		t.Fatalf("battery a: %+v", a)
	}
}

// A spec per battery: each found voltage becomes a reading, a missing second
// battery is skipped, a missing first one means another kind of packet.
func TestKaitaiDecoderSeveralBatteries(t *testing.T) {
	type frame struct{ V1, V2 uint16 }
	dec := func(f frame) (Decoded, error) {
		return kaitaiDecoder(func(*kaitai.Stream) (any, error) { return &f, nil },
			batterySpec{Battery: "a", Voltage: "V1", VoltageScale: 10, MinMV: 3000, MaxMV: 4500},
			batterySpec{Battery: "b", Voltage: "V2", VoltageScale: 10, MinMV: 3000, MaxMV: 4500})(nil)
	}
	d, err := dec(frame{V1: 400, V2: 395})
	if err != nil || len(d.Batteries) != 2 || d.Batteries[0].Battery != "a" || d.Batteries[1].VoltageMV != 3950 {
		t.Fatalf("%v, %+v", err, d)
	}
	if _, err := dec(frame{V1: 400, V2: 900}); !errors.Is(err, ErrImplausible) {
		t.Errorf("9 V on a 4.5 V battery gave %v", err)
	}
}
