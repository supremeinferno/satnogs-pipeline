package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/supremeinferno/orbitscreen/pkg/tle"
)

// celestrakURL is where current element sets come from; a variable so tests
// can point it at a local server. CelesTrak asks not to be queried more than
// once every couple of hours.
var celestrakURL = "https://celestrak.org/NORAD/elements/gp.php"

// fetchTLEText gets the current element set of one satellite as CelesTrak
// sends it: a name and two lines.
func fetchTLEText(ctx context.Context, norad int) (string, error) {
	url := fmt.Sprintf("%s?CATNR=%d&FORMAT=tle", celestrakURL, norad)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "satnogs-pipeline (learning project)")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return "", fmt.Errorf("get %s: %w", url, err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return string(body), err
}

// fetchTLE gets and parses the current element set of one satellite.
func fetchTLE(ctx context.Context, norad int) (*tle.TLE, error) {
	text, err := fetchTLEText(ctx, norad)
	if err != nil {
		return nil, err
	}
	return parseTLE(text)
}

// parseTLE reads the three-line form: a name, then the two element lines.
func parseTLE(text string) (*tle.TLE, error) {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimRight(l, " \r"); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 3 {
		// CelesTrak answers 200 with a short message when it has no such object.
		return nil, fmt.Errorf("expected a name and two element lines, got %d lines: %.80q", len(lines), text)
	}
	return tle.Parse(strings.TrimSpace(lines[0]), lines[1], lines[2])
}

// satnogsTLEURL is SatNOGS DB's own copy of the element sets; a variable so
// tests can point it at a local server.
var satnogsTLEURL = "https://db.satnogs.org/api/tle/"

// fetchSatnogsTLEText gets a satellite's newest element set from SatNOGS DB,
// as a name and two lines. No token is needed. It is the first choice because
// the pipeline already depends on SatNOGS, and it is reachable from GitHub's
// servers, where CelesTrak timed out for every satellite on the first run.
func fetchSatnogsTLEText(ctx context.Context, norad int) (string, error) {
	url := fmt.Sprintf("%s?norad_cat_id=%d&format=json", satnogsTLEURL, norad)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "satnogs-pipeline (learning project)")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return "", fmt.Errorf("get %s: %w", url, err)
	}
	var sets []struct {
		TLE0    string `json:"tle0"`
		TLE1    string `json:"tle1"`
		TLE2    string `json:"tle2"`
		Updated string `json:"updated"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&sets); err != nil {
		return "", fmt.Errorf("decode %s: %w", url, err)
	}
	if len(sets) == 0 {
		return "", fmt.Errorf("SatNOGS DB has no element set for satellite %d", norad)
	}
	best := sets[0]
	for _, s := range sets[1:] {
		if s.Updated > best.Updated { // RFC 3339 times sort as text
			best = s
		}
	}
	// SatNOGS stores the name line as "0 NAME", the form of the three-line format.
	return strings.TrimSpace(strings.TrimPrefix(best.TLE0, "0 ")) + "\n" + best.TLE1 + "\n" + best.TLE2 + "\n", nil
}

// fetchTLETextAny tries SatNOGS DB, then CelesTrak, each for at most 20
// seconds, and returns the first element set that parses.
func fetchTLETextAny(ctx context.Context, norad int) (string, error) {
	var errs []error
	for _, src := range []struct {
		name string
		get  func(context.Context, int) (string, error)
	}{{"SatNOGS DB", fetchSatnogsTLEText}, {"CelesTrak", fetchTLEText}} {
		sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		text, err := src.get(sctx, norad)
		cancel()
		if err == nil {
			if _, err = parseTLE(text); err == nil {
				if len(errs) > 0 {
					log.Printf("orbit data for %d from %s, after: %v", norad, src.name, errors.Join(errs...))
				}
				return text, nil
			}
		}
		errs = append(errs, fmt.Errorf("%s: %w", src.name, err))
	}
	return "", errors.Join(errs...)
}

// fetchTLEAny is fetchTLETextAny, parsed.
func fetchTLEAny(ctx context.Context, norad int) (*tle.TLE, error) {
	text, err := fetchTLETextAny(ctx, norad)
	if err != nil {
		return nil, err
	}
	return parseTLE(text)
}
