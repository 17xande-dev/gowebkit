package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
)

func ensureSetupToken(ctx context.Context, accounts *store.Store, log *slog.Logger, supplied string) error {
	count, err := accounts.UserCount(ctx)
	if err != nil || count != 0 {
		return err
	}

	token := supplied
	if token == "" {
		token = auth.NewToken()
	}
	issued, err := accounts.IssueSetupToken(ctx, auth.HashToken(token))
	if err != nil {
		return err
	}
	if issued {
		if supplied == "" {
			log.WarnContext(ctx, "no account exists; create the first admin at /setup", "setup_token", token)
		} else {
			log.WarnContext(ctx, "no account exists; use the configured token at /setup")
		}
		return nil
	}

	// Read after the guarded insert: another server may have issued or claimed
	// the token during startup. Never disclose or replace its credential.
	state, err := accounts.SetupState(ctx)
	if errors.Is(err, store.ErrNotFound) {
		count, countErr := accounts.UserCount(ctx)
		if countErr != nil || count != 0 {
			return countErr
		}
	}
	if err != nil {
		return err
	}
	switch {
	case state.Consumed:
		log.WarnContext(ctx, "setup token was spent; first-account setup cannot be repeated")
	case supplied != "" && state.TokenHash != auth.HashToken(supplied):
		log.WarnContext(ctx, "configured setup token does not match the token already issued")
	default:
		log.WarnContext(ctx, "setup token already issued; use the original token at /setup")
	}
	return nil
}
