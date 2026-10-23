package main

// Throwaway: what two new parsers give on real frames. Not committed.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/kaitai-io/kaitai_struct_go_runtime/kaitai"

	"github.com/supremeinferno/satnogs-pipeline/internal/decoders/alsat1n"
	"github.com/supremeinferno/satnogs-pipeline/internal/decoders/frontiersat"
)

func TestZZNew(t *testing.T) {
	dir := os.Getenv("NEW_DIR")
	if dir == "" {
		t.Skip()
	}
	for _, c := range []struct {
		norad  string
		fields []string
		parse  func(*kaitai.Stream) (any, error)
	}{
		{"41789", []string{"EpsVoltageBattery", "EpsCurrentBattery"}, func(s *kaitai.Stream) (any, error) { var v alsat1n.Alsat1n; return &v, v.Read(s, nil, &v) }},
		{"69015", []string{"EpsBatteryVoltageMv", "EpsBatteryPercent"}, func(s *kaitai.Stream) (any, error) { var v frontiersat.Frontiersat; return &v, v.Read(s, nil, &v) }},
	} {
		b, _ := os.ReadFile(dir + "/" + c.norad + ".json")
		var rows []struct{ Frame, Timestamp string }
		json.Unmarshal(b, &rows)
		ok, bad, panics := 0, 0, 0
		for _, r := range rows {
			f, _ := hex.DecodeString(r.Frame)
			func() {
				defer func() {
					if recover() != nil {
						panics++
					}
				}()
				p, err := c.parse(kaitai.NewStream(bytes.NewReader(f)))
				if err != nil {
					bad++
					return
				}
				v, has := findNumber(reflect.ValueOf(p), c.fields[0], 0)
				if !has {
					bad++
					return
				}
				ok++
				x, _ := findNumber(reflect.ValueOf(p), c.fields[1], 0)
				fmt.Printf("%s %s len=%d %s=%.0f %s=%.0f\n", c.norad, r.Timestamp, len(f), c.fields[0], v, c.fields[1], x)
			}()
		}
		fmt.Printf("%s: %d with the field, %d without or failed, %d panics, of %d\n", c.norad, ok, bad, panics, len(rows))
	}
}
