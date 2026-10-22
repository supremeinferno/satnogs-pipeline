package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

// alertPayload is the part of Alertmanager's webhook body we use.
type alertPayload struct {
	Alerts []struct {
		Status      string            `json:"status"` // firing or resolved
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"alerts"`
}

// alertHandler receives Alertmanager's webhook and passes one line per alert
// to out. Sending that line to Telegram or e-mail would go here.
func alertHandler(out func(format string, args ...any)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var p alertPayload
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		for _, a := range p.Alerts {
			out("ALERT %s: %s [%s] %s", strings.ToUpper(a.Status), a.Labels["alertname"], a.Labels["severity"], a.Annotations["summary"])
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// runNotify serves the webhook on addr until ctx is cancelled.
func runNotify(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.Handle("/alert", alertHandler(log.Printf))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
