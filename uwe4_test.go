package main

import (
	"bufio"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
)

// realFrames reads testdata/uwe4_frames.txt: real beacons from SatNOGS, keyed
// by the time a ground station received them.
func realFrames(t *testing.T) map[string][]byte {
	t.Helper()
	f, err := os.Open("testdata/uwe4_frames.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string][]byte{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ts, h, _ := strings.Cut(line, " ")
		b, err := hex.DecodeString(h)
		if err != nil {
			t.Fatalf("%s: %v", ts, err)
		}
		out[ts] = b
	}
	if len(out) != 3 {
		t.Fatalf("want 3 frames in testdata, got %d", len(out))
	}
	return out
}

// The expected values were decoded separately (in Python, from the same
// Kaitai layout), so this checks the Go code against a second reading of it.
func TestDecodeUWE4RealFrames(t *testing.T) {
	frames := realFrames(t)
	tests := []struct {
		ts   string
		want UWE4
	}{
		{"2026-10-22T02:06:42Z", UWE4{
			BattA:   Battery{VoltageMV: 3860, CurrentMA: -256, TempC: 16, ChargePct: 62},
			BattB:   Battery{VoltageMV: 3972, CurrentMA: -3, TempC: 15, ChargePct: 70},
			PowerMW: 955, OBCTempC: 17, UptimeS: 8971}},
		{"2026-10-22T00:34:57Z", UWE4{
			BattA:   Battery{VoltageMV: 3972, CurrentMA: -1, TempC: 16, ChargePct: 70},
			BattB:   Battery{VoltageMV: 3828, CurrentMA: -314, TempC: 15, ChargePct: 53},
			PowerMW: 1285, OBCTempC: 17, UptimeS: 3292}},
		{"2026-10-22T11:00:08Z", UWE4{
			BattA:   Battery{VoltageMV: 4108, CurrentMA: 118, TempC: 14, ChargePct: 80},
			BattB:   Battery{VoltageMV: 4148, CurrentMA: 162, TempC: 13, ChargePct: 87},
			PowerMW: 1093, OBCTempC: 17, UptimeS: 42133}},
	}
	for _, tt := range tests {
		t.Run(tt.ts, func(t *testing.T) {
			got, err := DecodeUWE4(frames[tt.ts])
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestDecodeUWE4Rejects(t *testing.T) {
	good := realFrames(t)["2026-10-22T11:00:08Z"]
	mutate := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), good...)) }

	tests := []struct {
		name  string
		frame []byte
		want  error
	}{
		{"too short", good[:27], ErrNotBeacon},
		{"too long", append(append([]byte(nil), good...), 0), ErrNotBeacon},
		{"empty", nil, ErrNotBeacon},
		{"other sender", mutate(func(b []byte) []byte { b[8] ^= 0x10; return b }), ErrNotBeacon},
		{"other API", mutate(func(b []byte) []byte { b[24] = 103; return b }), ErrNotBeacon},
		{"flipped payload bit", mutate(func(b []byte) []byte { b[55] ^= 0x01; return b }), ErrBadChecksum},
		{"flipped checksum bit", mutate(func(b []byte) []byte { b[73] ^= 0x80; return b }), ErrBadChecksum},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeUWE4(tt.frame); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// A frame cut short must never panic, whatever its length.
func TestDecodeUWE4NeverPanics(t *testing.T) {
	good := realFrames(t)["2026-10-22T11:00:08Z"]
	for n := 0; n <= len(good)+3; n++ {
		b := make([]byte, n)
		copy(b, good)
		DecodeUWE4(b) // only checking that it returns
	}
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }
