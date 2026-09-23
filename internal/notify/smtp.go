package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"

	"freewaypi/internal/i18n"
)

// SMTP sends events as mail.
//
// Offered because the old adapter had it, not because it is the recommended
// path: a webhook needs no credentials stored on the box and no third party
// that can withdraw password authentication and leave the alarms silent.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       []string
	// STARTTLS upgrades a plain connection. When false and the port is 465,
	// the connection is TLS from the start.
	STARTTLS bool
}

// Name identifies the channel.
func (s *SMTP) Name() string { return "e-post" }

// Send delivers one event.
func (s *SMTP) Send(ctx context.Context, e Event) error {
	if len(s.To) == 0 {
		return i18n.Errf("error.smtp.recipient")
	}
	addr := net.JoinHostPort(s.Host, fmt.Sprint(s.Port))

	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}

	if s.Port == 465 && !s.STARTTLS {
		conn = tls.Client(conn, &tls.Config{ServerName: s.Host})
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp: %w", err)
	}
	defer client.Close()

	if s.STARTTLS {
		if err := client.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
			return fmt.Errorf("smtp: starttls: %w", err)
		}
	}
	if s.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("smtp: login: %w", err)
		}
	}
	if err := client.Mail(s.From); err != nil {
		return fmt.Errorf("smtp: avsender: %w", err)
	}
	for _, to := range s.To {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("smtp: mottaker %s: %w", to, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if _, err := w.Write([]byte(s.message(e))); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	return client.Quit()
}

func (s *SMTP) message(e Event) string {
	subject := fmt.Sprintf("%s: %s", e.Unit, e.Title)
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", s.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(s.To, ", "))
	// Encoded, because a Norwegian subject line is not ASCII and a raw one
	// arrives as mojibake.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", e.Time.Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "%s\r\n\r\n", e.Message)
	fmt.Fprintf(&b, "Alvorlighet: %s\r\n", e.Level)
	fmt.Fprintf(&b, "Tidspunkt:   %s\r\n", e.Time.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "Aggregat:    %s\r\n", e.Unit)
	return b.String()
}
