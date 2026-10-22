package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// notifier delivers a message to a person.
type notifier interface {
	Notify(ctx context.Context, text string) error
}

// telegram sends messages through a Telegram bot.
type telegram struct {
	token  string
	chatID string
	base   string // https://api.telegram.org; replaced in tests
	client *http.Client
}

func newTelegram(token, chatID string) *telegram {
	return &telegram{token: token, chatID: chatID, base: "https://api.telegram.org", client: &http.Client{Timeout: 10 * time.Second}}
}

// maxMessage is Telegram's limit on a message's length, in characters.
const maxMessage = 4096

func (t *telegram) Notify(ctx context.Context, text string) error {
	if r := []rune(text); len(r) > maxMessage {
		text = string(r[:maxMessage-1]) + "…"
	}
	form := url.Values{"chat_id": {t.chatID}, "text": {text}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/bot"+t.token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return t.redact(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.client.Do(req)
	if err != nil {
		return t.redact(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: status %s", resp.Status)
	}
	return nil
}

// redact removes the bot token from an error. The token is part of the
// request URL and net/http prints the URL in its errors, which would put the
// token in the logs.
func (t *telegram) redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("telegram: %s: %w", ue.Op, errors.New(strings.ReplaceAll(ue.Err.Error(), t.token, "<token>")))
	}
	return errors.New(strings.ReplaceAll(err.Error(), t.token, "<token>"))
}

// formatAlert is the text of one alert as the person reads it.
func formatAlert(status, name, severity, summary string) string {
	icon := "🔴"
	if status == "resolved" {
		icon = "✅"
	}
	return fmt.Sprintf("%s %s: %s [%s]\n%s", icon, strings.ToUpper(status), name, severity, summary)
}

// alertPayload is the part of Alertmanager's webhook body we use.
type alertPayload struct {
	Alerts []struct {
		Status      string            `json:"status"` // firing or resolved
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"alerts"`
}

// alertHandler receives Alertmanager's webhook, logs one line per alert, and
// passes each to n if there is one. If any delivery fails it answers 502, so
// Alertmanager sends the batch again; an alert may then arrive twice, which
// is better than not at all.
func alertHandler(logf func(format string, args ...any), n notifier) http.Handler {
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
		failed := false
		for _, a := range p.Alerts {
			name, sev, sum := a.Labels["alertname"], a.Labels["severity"], a.Annotations["summary"]
			logf("ALERT %s: %s [%s] %s", strings.ToUpper(a.Status), name, sev, sum)
			if n == nil {
				continue
			}
			if err := n.Notify(r.Context(), formatAlert(a.Status, name, sev, sum)); err != nil {
				logf("could not deliver alert %s: %v", name, err)
				failed = true
			}
		}
		if failed {
			http.Error(w, "delivery failed", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// runNotify serves the webhook on addr until ctx is cancelled.
func runNotify(ctx context.Context, addr, telegramToken, telegramChat string) error {
	var n notifier
	if telegramToken != "" && telegramChat != "" {
		n = newTelegram(telegramToken, telegramChat)
		log.Print("alerts go to Telegram")
	} else {
		log.Print("TELEGRAM_TOKEN or TELEGRAM_CHAT_ID not set: alerts are only logged")
	}
	mux := http.NewServeMux()
	mux.Handle("/alert", alertHandler(log.Printf, n))
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
