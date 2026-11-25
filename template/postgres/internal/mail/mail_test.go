package mail_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/mail"
	"github.com/17xande-dev/mailer"
)

func TestNewDiscardsWithoutRelay(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	sender, enabled, err := mail.New(mailer.SMTPConfig{}, log)
	if err != nil || enabled {
		t.Fatalf("no relay: enabled=%v error=%v", enabled, err)
	}
	if !strings.Contains(logs.String(), "mail is off") {
		t.Fatalf("startup did not say mail is off: %s", &logs)
	}
	logs.Reset()
	if err := sender.Send(t.Context(), mailer.Message{To: []string{"recipient@example.com"}, Subject: "s", Text: "t"}); err != nil {
		t.Fatalf("discarding sender failed: %v", err)
	}
	// The count is logged; the address is not.
	if !strings.Contains(logs.String(), "discarding message") || !strings.Contains(logs.String(), "recipients=1") ||
		strings.Contains(logs.String(), "recipient@example.com") {
		t.Fatalf("discarded message log: %s", &logs)
	}
}

func TestNewBuildsSMTPSenderWithRelay(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	sender, enabled, err := mail.New(mailer.SMTPConfig{
		Host: "relay.example", Port: 587, From: "sender@example.com", TLS: mailer.TLSStartTLS,
	}, log)
	if err != nil || !enabled {
		t.Fatalf("relay: enabled=%v error=%v", enabled, err)
	}
	if _, ok := sender.(mailer.Discard); ok || sender == nil {
		t.Fatalf("a configured relay still discards mail: %T", sender)
	}
	if _, _, err := mail.New(mailer.SMTPConfig{Host: "relay.example", Port: 587, From: ""}, log); err == nil {
		t.Fatal("invalid relay settings were accepted")
	}
}
