// Package jobs registers the application's background work.
package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/db"
	"github.com/17xande-dev/mailer"
	"github.com/17xande-dev/outbox"
)

// Queue uses the transaction type of this application's database.
type Queue = outbox.Queue[queueTx]

// MailTest is the kind for the administrator's test message. Its payload is
// the recipient address captured when the job was enqueued.
const MailTest = "mail-test"

var invalidAddress = errors.New("Invalid recipient address.")
var smtpRefusal = errors.New("The mail server permanently refused the message.")

// New constructs the queue. Start it only after the database is migrated.
func New(d *db.DB, sender mailer.Sender, siteName string, opts outbox.Options) (*Queue, error) {
	opts.Describe = func(err error) string {
		// SMTP diagnostics can contain credentials and addresses. Persist
		// fixed descriptions so the admin page never renders those details.
		switch {
		case errors.Is(err, invalidAddress):
			return invalidAddress.Error()
		case errors.Is(err, smtpRefusal):
			return smtpRefusal.Error()
		default:
			return "Job failed; see the server log for its error class."
		}
	}
	return outbox.New(storage(d), map[string]outbox.Kind[queueTx]{
		MailTest: {
			Timeout: func(context.Context, outbox.Job) time.Duration { return 20 * time.Second },
			Run: func(ctx context.Context, job outbox.Job) error {
				message := mailer.Message{
					To:      []string{string(job.Payload)},
					Subject: "Test message",
					Text:    "This is a test message from " + siteName + ". Mail settings are working.",
				}
				if err := message.Validate(); err != nil {
					return outbox.Permanent(invalidAddress)
				}
				err := sender.Send(ctx, message)
				if err == nil {
					return nil
				}
				if ctx.Err() != nil {
					// The queue refunds an attempt canceled by shutdown.
					return ctx.Err()
				}
				// The SMTP sender wraps a typed SMTP reply. A 5xx reply is
				// permanent; 4xx replies and network errors can be retried.
				if reply, ok := errors.AsType[interface {
					error
					ErrorCode() int
				}](err); ok && reply.ErrorCode()/100 == 5 {
					return outbox.Permanent(errors.Join(smtpRefusal, err))
				}
				return err
			},
		},
	}, opts)
}
