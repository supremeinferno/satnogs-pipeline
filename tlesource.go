package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/supremeinferno/orbitscreen/pkg/tle"
)

// celestrakURL is where current element sets come from; a variable so tests
// can point it at a local server. CelesTrak asks not to be queried more than
// once every couple of hours.
var celestrakURL = "https://celestrak.org/NORAD/elements/gp.php"

// fetchTLE gets the current element set of one satellite.
func fetchTLE(ctx context.Context, norad int) (*tle.TLE, error) {
	url := fmt.Sprintf("%s?CATNR=%d&FORMAT=tle", celestrakURL, norad)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "satnogs-pipeline (learning project)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, fmt.Errorf("get %s: %w", url, err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, err
	}
	return parseTLE(string(body))
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
