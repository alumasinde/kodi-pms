package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
)

var ErrNotConfigured = errors.New("smtp is not configured")

type Mailer struct {
	addr string
	auth smtp.Auth
	from string
	log  *slog.Logger
}

// New accepts smtp://user:pass@host:port. Port 587 with STARTTLS is expected;
// implicit TLS (465) is not supported by net/smtp.
func New(rawURL, from string, log *slog.Logger) (*Mailer, error) {
	rawURL = strings.TrimSpace(rawURL)
	from = strings.TrimSpace(from)
	m := &Mailer{log: log}
	if rawURL == "" {
		if from != "" {
			return nil, fmt.Errorf("SMTP_URL is required when MAIL_FROM is set")
		}
		return m, nil
	}
	if from == "" {
		return nil, fmt.Errorf("MAIL_FROM is required when SMTP_URL is set")
	}
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("MAIL_FROM: %w", err)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("SMTP_URL: %w", err)
	}
	if u.Scheme != "smtp" {
		return nil, fmt.Errorf("SMTP_URL must use smtp://")
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("SMTP_URL must include a host")
	}
	port := u.Port()
	if port == "" {
		port = "587"
	}
	m.addr = net.JoinHostPort(host, port)
	m.from = addr.Address
	if u.User != nil {
		user := u.User.Username()
		pass, _ := u.User.Password()
		if user != "" {
			m.auth = smtp.PlainAuth("", user, pass, host)
		}
	}
	return m, nil
}

func (m *Mailer) Enabled() bool { return m != nil && m.addr != "" }

func (m *Mailer) Send(ctx context.Context, to, subject, body string) error {
	if !m.Enabled() {
		return ErrNotConfigured
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(to))
	if err != nil {
		return fmt.Errorf("recipient: %w", err)
	}
	if !headerSafe(addr.Address) || !headerSafe(subject) || !headerSafe(m.from) {
		return fmt.Errorf("mail header contains a newline")
	}
	msg := strings.Join([]string{
		"From: " + m.from,
		"To: " + addr.Address,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
	}, "\r\n")

	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(m.addr, m.auth, m.from, []string{addr.Address}, []byte(msg))
	}()
	select {
	case err := <-done:
		if err != nil {
			m.log.Error("send email", "error", err)
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func headerSafe(v string) bool {
	return !strings.ContainsAny(v, "\r\n")
}
