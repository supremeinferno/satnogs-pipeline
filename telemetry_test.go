package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchTelemetryPage(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"next":"https://example.org/next","previous":null,"results":[
			{"norad_cat_id":43880,"frame":"abcd","timestamp":"2026-10-22T11:00:08Z","observation_id":7,"station_id":9,"decoded":"influxdb","observer":"x"}]}`))
	}))
	defer srv.Close()

	frames, next, err := fetchTelemetryPage(context.Background(), srv.URL, "TOK")
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Token TOK" {
		t.Errorf("Authorization = %q", auth)
	}
	if next != "https://example.org/next" {
		t.Errorf("next = %q", next)
	}
	want := TelemetryFrame{NoradID: 43880, Frame: "abcd", Timestamp: time.Date(2026, 10, 22, 11, 0, 8, 0, time.UTC), ObservationID: 7, StationID: 9}
	if len(frames) != 1 || frames[0] != want {
		t.Fatalf("frames = %+v, want [%+v]", frames, want)
	}
}

func TestFetchTelemetryRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, _, err := fetchTelemetryPage(context.Background(), srv.URL, "TOK")
	var rl rateLimitError
	if !errors.As(err, &rl) || rl.after != 2*time.Minute {
		t.Fatalf("err = %v, want rateLimitError of 2m", err)
	}
}

func TestCheckStatusRetryAfterDefaultsAndCaps(t *testing.T) {
	for _, tt := range []struct {
		header string
		want   time.Duration
	}{
		{"", time.Minute},
		{"junk", time.Minute},
		{"30", 30 * time.Second},
		{"86400", 15 * time.Minute},
	} {
		rec := httptest.NewRecorder()
		if tt.header != "" {
			rec.Header().Set("Retry-After", tt.header)
		}
		rec.WriteHeader(http.StatusTooManyRequests)
		var rl rateLimitError
		if err := checkStatus(rec.Result()); !errors.As(err, &rl) || rl.after != tt.want {
			t.Errorf("Retry-After %q: err = %v, want %s", tt.header, err, tt.want)
		}
	}
}

func TestFreshFrames(t *testing.T) {
	at := time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC)
	a := TelemetryFrame{Frame: "aa", Timestamp: at}
	b := TelemetryFrame{Frame: "bb", Timestamp: at}
	aAgain := TelemetryFrame{Frame: "aa", Timestamp: at.Add(6 * time.Second)} // same bytes, heard by another station
	seen := map[string]bool{"aa@" + at.Format(time.RFC3339): true}

	got := freshFrames([]TelemetryFrame{a, b, aAgain}, seen)
	if len(got) != 2 || got[0] != b || got[1] != aAgain {
		t.Fatalf("got %+v", got)
	}
}

// message builds what fetch-telemetry puts on the queue.
func message(t *testing.T, tf TelemetryFrame) []byte {
	t.Helper()
	b, err := json.Marshal(tf)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeTelemetryMessage(t *testing.T) {
	frames := realFrames(t)
	at := time.Date(2026, 10, 22, 11, 0, 8, 0, time.UTC)
	good := TelemetryFrame{NoradID: uwe4NORAD, Timestamp: at, ObservationID: 1, StationID: 2}
	good.Frame = hexOf(frames["2026-10-22T11:00:08Z"])

	// A real beacon decodes.
	_, raw, r, err := decodeTelemetryMessage(message(t, good))
	if err != nil || len(raw) != 74 || r.BattA.VoltageMV != 4108 {
		t.Fatalf("good message: err %v, %d bytes, reading %+v", err, len(raw), r)
	}

	// A frame without an observation id (uploaded outside SatNOGS Network) is fine.
	noObs := TelemetryFrame{NoradID: uwe4NORAD, Frame: good.Frame, Timestamp: at}
	if _, _, _, err := decodeTelemetryMessage(message(t, noObs)); err != nil {
		t.Fatalf("frame without observation id: %v", err)
	}

	tests := []struct {
		name     string
		body     []byte
		wantPerm bool
		want     error
	}{
		{"not json", []byte("garbage"), true, nil},
		{"other satellite", message(t, TelemetryFrame{NoradID: 25544, Frame: good.Frame, Timestamp: at, ObservationID: 1}), true, nil},
		{"no time", message(t, TelemetryFrame{NoradID: uwe4NORAD, Frame: good.Frame, ObservationID: 1}), true, nil},
		{"not hex", message(t, TelemetryFrame{NoradID: uwe4NORAD, Frame: "zz", Timestamp: at, ObservationID: 1}), true, nil},
		{"short frame", message(t, TelemetryFrame{NoradID: uwe4NORAD, Frame: "abcd", Timestamp: at, ObservationID: 1}), false, ErrNotBeacon},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := decodeTelemetryMessage(tt.body)
			var perm permanentError
			if got := errors.As(err, &perm); got != tt.wantPerm {
				t.Fatalf("permanent = %v, want %v (err %v)", got, tt.wantPerm, err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// A poll that fails part-way must not leave a hole: the next poll has to read
// on past pages that look "already seen" from the top.
func TestPollTelemetryCatchesUpAfterPartialFailure(t *testing.T) {
	telemetryPageDelay = 0
	defer func() { telemetryPageDelay = 6 * time.Second }()

	at := time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC)
	frame := func(hex string) string {
		return `{"norad_cat_id":43880,"frame":"` + hex + `","timestamp":"` + at.Format(time.RFC3339) + `","observation_id":1,"station_id":1}`
	}
	var failPage3 bool
	var requests []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		switch r.URL.Path {
		case "/p1":
			w.Write([]byte(`{"next":"` + srv.URL + `/p2","results":[` + frame("01") + `,` + frame("02") + `]}`))
		case "/p2":
			w.Write([]byte(`{"next":"` + srv.URL + `/p3","results":[` + frame("03") + `,` + frame("04") + `]}`))
		case "/p3":
			if failPage3 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Write([]byte(`{"next":null,"results":[` + frame("05") + `,` + frame("06") + `]}`))
		}
	}))
	defer srv.Close()

	var published []string
	publish := func(_ context.Context, _ string, v any) error {
		published = append(published, v.(TelemetryFrame).Frame)
		return nil
	}
	seen := map[string]bool{}
	var partial bool
	poll := func() error {
		return pollTelemetry(context.Background(), publish, "TOK", srv.URL+"/p1", 10, seen, &partial)
	}

	// Poll 1 gets through two pages, then page 3 fails.
	failPage3 = true
	if err := poll(); err == nil {
		t.Fatal("poll 1 should fail")
	}
	if got := len(published); got != 4 {
		t.Fatalf("poll 1 published %d frames, want 4", got)
	}

	// Poll 2: page 1 has nothing new, but it must go on to page 3.
	failPage3 = false
	if err := poll(); err != nil {
		t.Fatal(err)
	}
	if got := len(published); got != 6 {
		t.Fatalf("after poll 2 published %d frames, want 6 (page 3 was skipped?)", got)
	}

	// Poll 3: caught up, so it stops at page 1.
	requests = nil
	if err := poll(); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || len(published) != 6 {
		t.Fatalf("poll 3 made requests %v and published %d; want one request, none new", requests, len(published))
	}
}

// A 429 in the middle of a poll must be waited out on that page, not turned
// into a failed poll that starts over from page one.
func TestPollTelemetryWaitsOutRateLimit(t *testing.T) {
	telemetryPageDelay = 0
	rateLimitSlack = 0
	defer func() { telemetryPageDelay = 6 * time.Second; rateLimitSlack = time.Second }()

	at := time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC).Format(time.RFC3339)
	f := func(h string) string {
		return `{"norad_cat_id":43880,"frame":"` + h + `","timestamp":"` + at + `","observation_id":1,"station_id":1}`
	}
	hits := map[string]int{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/p1":
			w.Write([]byte(`{"next":"` + srv.URL + `/p2","results":[` + f("01") + `]}`))
		case "/p2":
			if hits["/p2"] == 1 { // limited the first time only
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.Write([]byte(`{"next":null,"results":[` + f("02") + `]}`))
		}
	}))
	defer srv.Close()

	var published []string
	publish := func(_ context.Context, _ string, v any) error {
		published = append(published, v.(TelemetryFrame).Frame)
		return nil
	}
	var partial bool
	err := pollTelemetry(context.Background(), publish, "TOK", srv.URL+"/p1", 10, map[string]bool{}, &partial)
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 2 || hits["/p1"] != 1 || hits["/p2"] != 2 {
		t.Fatalf("published %v, hits %v; want both frames, page 1 read once, page 2 twice", published, hits)
	}
}
