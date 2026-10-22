package main

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// UWE-4 (NORAD 43880) is a CubeSat from the University of Würzburg. Every few
// minutes it radios a 74-byte housekeeping beacon. The layout below follows
// SatNOGS's Kaitai definition (satnogs-decoders, ksy/uwe4.ksy).
//
//	bytes  0..15  AX.25 header: destination, source, control, protocol
//	bytes 16..25  beacon header (system ids, API number, payload size)
//	bytes 26..71  housekeeping payload, numbers little-endian
//	bytes 72..73  checksum of bytes 16..71
const (
	uwe4FrameLen   = 74
	uwe4Source     = "DP0UWH" // the satellite's amateur radio callsign
	uwe4API        = 14       // housekeeping beacon (103 is a text message)
	uwe4PayloadLen = 46

	offHeader   = 16
	offChecksum = 72
)

// Byte offsets of the housekeeping fields in the frame.
const (
	offUptime    = 43
	offBattATemp = 49
	offBattASoC  = 50
	offBattBTemp = 51
	offBattBSoC  = 52
	offBattAmA   = 53
	offBattAmV   = 55
	offBattBmA   = 57
	offBattBmV   = 59
	offPowerMW   = 61
	offOBCTemp   = 63
)

var (
	// ErrNotBeacon means the frame is valid radio traffic but not a
	// housekeeping beacon: another length, another sender, a text message.
	// Skip it; nothing is wrong.
	ErrNotBeacon = errors.New("not a UWE-4 housekeeping beacon")
	// ErrBadChecksum means it looks like a beacon but arrived damaged.
	ErrBadChecksum = errors.New("UWE-4 beacon fails its checksum")
)

// Battery is one of UWE-4's two batteries.
type Battery struct {
	VoltageMV int // millivolts
	CurrentMA int // milliamps: positive charging, negative discharging
	TempC     int // degrees Celsius
	ChargePct int // state of charge, percent
}

// UWE4 is one decoded housekeeping beacon.
type UWE4 struct {
	BattA, BattB Battery
	PowerMW      int // milliwatts
	OBCTempC     int // on-board computer temperature
	UptimeS      uint32
}

// DecodeUWE4 decodes one frame as received (the bytes SatNOGS stores).
func DecodeUWE4(f []byte) (UWE4, error) {
	if len(f) != uwe4FrameLen {
		return UWE4{}, fmt.Errorf("%w: %d bytes, want %d", ErrNotBeacon, len(f), uwe4FrameLen)
	}
	if got := callsign(f[7:13]); got != uwe4Source {
		return UWE4{}, fmt.Errorf("%w: sent by %q", ErrNotBeacon, got)
	}
	// Control 0x03 and protocol 0xF0: an unnumbered information frame.
	if f[14] != 0x03 || f[15] != 0xF0 {
		return UWE4{}, fmt.Errorf("%w: control %#x, protocol %#x", ErrNotBeacon, f[14], f[15])
	}
	// System ids 2.1 -> 1.0, API number and payload size, as in the Kaitai file.
	if f[20] != 2 || f[21] != 1 || f[22] != 1 || f[23] != 0 || f[24] != uwe4API || f[25] != uwe4PayloadLen {
		return UWE4{}, fmt.Errorf("%w: API %d, payload size %d", ErrNotBeacon, f[24], f[25])
	}
	if fletcher16(f[offHeader:offChecksum]) != binary.LittleEndian.Uint16(f[offChecksum:]) {
		return UWE4{}, ErrBadChecksum
	}

	s1 := func(off int) int { return int(int8(f[off])) }
	s2 := func(off int) int { return int(int16(binary.LittleEndian.Uint16(f[off:]))) }
	return UWE4{
		BattA:    Battery{VoltageMV: s2(offBattAmV), CurrentMA: s2(offBattAmA), TempC: s1(offBattATemp), ChargePct: s1(offBattASoC)},
		BattB:    Battery{VoltageMV: s2(offBattBmV), CurrentMA: s2(offBattBmA), TempC: s1(offBattBTemp), ChargePct: s1(offBattBSoC)},
		PowerMW:  s2(offPowerMW),
		OBCTempC: s1(offOBCTemp),
		UptimeS:  binary.LittleEndian.Uint32(f[offUptime:]),
	}, nil
}

// callsign reads an AX.25 address: six characters, each stored rotated left
// by one bit, padded with spaces.
func callsign(b []byte) string {
	out := make([]byte, 0, len(b))
	for _, x := range b {
		if c := x>>1 | x<<7; c != ' ' {
			out = append(out, c)
		}
	}
	return string(out)
}

// fletcher16 is the checksum UWE-4 uses: two running sums, each wrapping at
// 256, the second adding up the first. The result is sum1 | sum2<<8. (The
// Kaitai file only says "Fletcher's algorithm"; this variant matches every
// real frame tried.)
func fletcher16(data []byte) uint16 {
	var a, b byte
	for _, x := range data {
		a += x
		b += a
	}
	return uint16(a) | uint16(b)<<8
}
