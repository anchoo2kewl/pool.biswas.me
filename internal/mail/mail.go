// Package mail sends transactional email through a go-email gateway.
//
// The gateway owns the SMTP relationship, the per-key rate limits and the
// delivery log; this app owns the words. That split is why there is no SMTP
// configuration here — a bearer key and a URL is the whole of it.
package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNotConfigured is returned when no gateway is set up. It is a normal
// condition rather than a failure: an instance with no mail simply cannot
// offer the features that need it, and should say so plainly.
var ErrNotConfigured = errors.New("mail: no gateway configured")

// Sender talks to one go-email gateway.
type Sender struct {
	BaseURL string
	APIKey  string
	// From is the envelope sender. The gateway decides whether this address is
	// one the key may send as.
	From     string
	FromName string
	HTTP     *http.Client
}

// New builds a sender. An empty URL or key yields one that reports itself as
// unconfigured rather than failing at the point of use.
func New(baseURL, apiKey, from, fromName string) *Sender {
	return &Sender{
		BaseURL:  strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:   strings.TrimSpace(apiKey),
		From:     strings.TrimSpace(from),
		FromName: strings.TrimSpace(fromName),
		// Short, because a person is waiting on the other side of this.
		HTTP: &http.Client{Timeout: 20 * time.Second},
	}
}

// Configured reports whether mail can be sent.
func (s *Sender) Configured() bool {
	return s != nil && s.BaseURL != "" && s.APIKey != "" && s.From != ""
}

type address struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type message struct {
	From    address   `json:"from"`
	To      []address `json:"to"`
	Subject string    `json:"subject"`
	Text    string    `json:"text"`
}

// Send delivers a plain-text message.
func (s *Sender) Send(ctx context.Context, to, subject, text string) error {
	if !s.Configured() {
		return ErrNotConfigured
	}

	body, err := json.Marshal(message{
		From:    address{Email: s.From, Name: s.FromName},
		To:      []address{{Email: to}},
		Subject: subject,
		Text:    text,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+"/v1/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.APIKey)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("mail: reaching the gateway: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// The gateway's own message is worth keeping: it is the difference between
	// "over your daily limit" and "that sender is not yours".
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
	return fmt.Errorf("mail: gateway returned %s: %s", resp.Status, strings.TrimSpace(string(detail)))
}
