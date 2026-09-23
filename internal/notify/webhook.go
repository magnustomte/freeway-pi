package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Webhook posts events as JSON.
//
// The primary channel on purpose. Taken into Homey it becomes a flow, and from
// there a push notification, or whatever else somebody wants to build on it —
// without this box needing to know how to send mail.
type Webhook struct {
	Label   string
	URL     string
	Headers map[string]string

	client *http.Client
}

// NewWebhook returns a channel posting to url.
func NewWebhook(label, url string, headers map[string]string) *Webhook {
	if label == "" {
		label = "webhook"
	}
	return &Webhook{
		Label: label, URL: url, Headers: headers,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Name identifies the channel.
func (w *Webhook) Name() string { return w.Label }

// Send posts the event.
func (w *Webhook) Send(ctx context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "freeway-pi")
	for k, v := range w.Headers {
		req.Header.Set(k, v)
	}

	res, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	defer res.Body.Close()
	// Drained so the connection can be reused, and capped because a server
	// that answers with a megabyte of HTML should not be quoted in full.
	snippet, _ := io.ReadAll(io.LimitReader(res.Body, 512))

	if res.StatusCode >= 300 {
		return fmt.Errorf("webhook: %s svarte %s: %s", w.URL, res.Status, bytes.TrimSpace(snippet))
	}
	return nil
}
