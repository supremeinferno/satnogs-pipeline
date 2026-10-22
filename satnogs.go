package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Observation is one ground-station pass over one satellite. Only the fields
// the pipeline needs are listed; encoding/json ignores the other ~45.
type Observation struct {
	ID        int       `json:"id"`
	NoradID   int       `json:"norad_cat_id"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Status    string    `json:"status"`  // good, bad, failed, future, unknown
	Payload   *string   `json:"payload"` // audio URL; null until the pass is done
	DemodData []Demod   `json:"demoddata"`
}

// Demod points at one file of demodulated frames for an observation.
type Demod struct {
	URL string `json:"payload_demod"`
}

// client has a timeout: http.Get uses http.DefaultClient, which waits forever.
var client = &http.Client{Timeout: 30 * time.Second}

// observationsURL is the first page of the API, newest observations first,
// for passes that started before the given time. Without that cutoff the API
// opens with every pass scheduled for the next days (over 100 for the ISS),
// all "future", and a poll would spend its page budget on them.
func observationsURL(norad int, before time.Time) string {
	u := "https://network.satnogs.org/api/observations/?format=json&start__lt=" + before.UTC().Format(time.RFC3339)
	if norad > 0 {
		u += fmt.Sprintf("&norad_cat_id=%d", norad)
	}
	return u
}

// fetchPage reads one page of observations. next is the URL of the following
// page, or "" on the last one.
func fetchPage(ctx context.Context, url string) (obs []Observation, next string, err error) {
	defer func() {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		apiRequests.WithLabelValues(outcome).Inc()
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "satnogs-pipeline (learning project)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("get %s: status %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&obs); err != nil {
		return nil, "", fmt.Errorf("decode %s: %w", url, err)
	}
	return obs, nextLink(resp.Header.Get("Link")), nil
}

// nextLink picks the rel="next" URL out of a Link header such as
//
//	<https://host/api/x/?cursor=abc>; rel="next", <https://host/api/x/?cursor=xyz>; rel="prev"
//
// and returns "" if there isn't one.
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		if end := strings.Index(part, ">"); strings.HasPrefix(part, "<") && end > 0 {
			return part[1:end]
		}
	}
	return ""
}

// keep drops observations that haven't happened yet. The API's status filter
// doesn't remove them, so we check ourselves.
func keep(obs []Observation) []Observation {
	var out []Observation
	for _, o := range obs {
		if o.Status != "future" {
			out = append(out, o)
		}
	}
	return out
}
