package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestKeep(t *testing.T) {
	in := []Observation{
		{ID: 1, Status: "good"},
		{ID: 2, Status: "future"},
		{ID: 3, Status: "bad"},
		{ID: 4, Status: "failed"},
		{ID: 5, Status: "future"},
		{ID: 6, Status: "unknown"},
	}
	var ids []int
	for _, o := range keep(in) {
		ids = append(ids, o.ID)
	}
	if want := []int{1, 3, 4, 6}; !slices.Equal(ids, want) {
		t.Fatalf("kept ids %v, want %v", ids, want)
	}
}

func TestKeepEmpty(t *testing.T) {
	if got := keep(nil); len(got) != 0 {
		t.Fatalf("keep(nil) = %v, want empty", got)
	}
}

func TestNextLink(t *testing.T) {
	tests := []struct {
		name, header, want string
	}{
		{"next only", `<https://h/api/o/?cursor=abc&format=json>; rel="next"`, "https://h/api/o/?cursor=abc&format=json"},
		{"next and prev", `<https://h/p>; rel="prev", <https://h/n>; rel="next"`, "https://h/n"},
		{"prev only", `<https://h/p>; rel="prev"`, ""},
		{"no header", ``, ""},
		{"garbage", `rel="next"`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextLink(tt.header); got != tt.want {
				t.Fatalf("nextLink(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestObservationsURL(t *testing.T) {
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	got := observationsURL(25544, at)
	for _, want := range []string{"norad_cat_id=25544", "start__lt=2026-10-22T08:30:00Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("observationsURL = %q, missing %q", got, want)
		}
	}
	if strings.Contains(observationsURL(0, at), "norad_cat_id") {
		t.Error("norad 0 should not filter by satellite")
	}
}
