package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestFresh(t *testing.T) {
	seen := map[int]fingerprint{
		1: {"good", 1},
		2: {"unknown", 0},
	}
	in := []Observation{
		{ID: 1, Status: "good", DemodData: []Demod{{}}},    // same: skipped
		{ID: 2, Status: "good"},                            // status changed
		{ID: 3, Status: "unknown"},                         // never seen
		{ID: 4, Status: "unknown", DemodData: []Demod{{}}}, // never seen
	}
	var ids []int
	for _, o := range fresh(in, seen) {
		ids = append(ids, o.ID)
	}
	if want := []int{2, 3, 4}; !slices.Equal(ids, want) {
		t.Fatalf("fresh ids %v, want %v", ids, want)
	}
}

func TestValidate(t *testing.T) {
	start := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	good := Observation{ID: 1, NoradID: 25544, Status: "good", Start: start, End: start.Add(time.Minute)}
	if err := validate(good); err != nil {
		t.Fatalf("valid observation rejected: %v", err)
	}
	bad := []Observation{
		{},
		{ID: 1, NoradID: 25544, Start: start, End: start.Add(time.Minute)},                  // no status
		{ID: 1, NoradID: 25544, Status: "good", Start: start, End: start.Add(-time.Minute)}, // ends before start
		{ID: 0, NoradID: 25544, Status: "good", Start: start, End: start.Add(time.Minute)},  // no id
	}
	for i, o := range bad {
		if validate(o) == nil {
			t.Errorf("case %d: invalid observation accepted: %+v", i, o)
		}
	}
}

func TestAlertHandler(t *testing.T) {
	var lines []string
	h := alertHandler(func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }, nil)

	body := `{"alerts":[
		{"status":"firing","labels":{"alertname":"DeadQueueNotEmpty","severity":"warning"},"annotations":{"summary":"2 message(s) in the dead queue"}},
		{"status":"resolved","labels":{"alertname":"ServiceDown","severity":"critical"},"annotations":{"summary":"store is down"}}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/alert", strings.NewReader(body)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", rec.Code)
	}
	want := []string{
		"ALERT FIRING: DeadQueueNotEmpty [warning] 2 message(s) in the dead queue",
		"ALERT RESOLVED: ServiceDown [critical] store is down",
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines %q, want %q", lines, want)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/alert", strings.NewReader("not json")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: status %d, want 400", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/alert", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status %d, want 405", rec.Code)
	}
}

func TestTelegramNotify(t *testing.T) {
	var path, chat, text string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		path, chat, text = r.URL.Path, r.PostForm.Get("chat_id"), r.PostForm.Get("text")
	}))
	defer srv.Close()

	tg := newTelegram("SECRET123", "42")
	tg.base = srv.URL
	if err := tg.Notify(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if path != "/botSECRET123/sendMessage" || chat != "42" || text != "hello" {
		t.Fatalf("got path %q chat %q text %q", path, chat, text)
	}
}

func TestTelegramErrorHidesToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // nothing listens any more: the request fails with the URL in the error

	tg := newTelegram("SECRET123", "42")
	tg.base = base
	err := tg.Notify(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "SECRET123") {
		t.Fatalf("error leaks the token: %v", err)
	}
}

type failingNotifier struct{}

func (failingNotifier) Notify(context.Context, string) error { return errors.New("down") }

func TestAlertHandlerDeliveryFailure(t *testing.T) {
	h := alertHandler(func(string, ...any) {}, failingNotifier{})
	body := `{"alerts":[{"status":"firing","labels":{"alertname":"X"},"annotations":{}}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/alert", strings.NewReader(body)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 so Alertmanager retries", rec.Code)
	}
}

func TestFormatAlert(t *testing.T) {
	got := formatAlert("resolved", "ServiceDown", "critical", "store is down")
	if want := "✅ RESOLVED: ServiceDown [critical]\nstore is down"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
