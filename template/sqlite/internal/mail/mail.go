// Package mail chooses the application's outgoing mail transport.
package mail

import (
	"context"
	"log/slog"

	"github.com/17xande-dev/mailer"
)

// New returns an SMTP sender when a relay is configured and otherwise one that
// discards messages. Mail is optional, so a deployment without a relay must
// still run and its admin page must say so instead of failing requests.
// The second result reports whether messages are really delivered.
func New(cfg mailer.SMTPConfig, log *slog.Logger) (sender mailer.Sender, enabled bool, err error) {
	if cfg.Host == "" {
		log.Info("mail is off: SMTP_HOST is not set, so messages are discarded")
		return Discard(log), false, nil
	}
	s, err := mailer.NewSMTPSender(cfg)
	if err != nil {
		return nil, false, err
	}
	return s, true, nil
}

// Discard returns the sender for a deployment with no relay. It reports success
// and logs the subject and the number of recipients, never an address: the
// library's own discarding sender logs the recipients, and the application's
// rule is that email addresses stay out of the logs.
func Discard(log *slog.Logger) mailer.Sender { return discard{log: log} }

type discard struct{ log *slog.Logger }

func (d discard) Send(ctx context.Context, m mailer.Message) error {
	d.log.WarnContext(ctx, "email not configured; discarding message", "subject", m.Subject, "recipients", len(m.To))
	return nil
}
