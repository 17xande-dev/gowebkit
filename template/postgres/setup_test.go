package main

import (
	"encoding/json"
	"testing"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/dbtest"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
	"github.com/17xande-dev/websec/webtest"
)

func TestSetupTokenStartup(t *testing.T) {
	const supplied = "configured-credential-for-first-account"
	const original = "original-credential-for-first-account"
	for _, tt := range []struct {
		name, initial, configured, message string
		spent                              bool
	}{
		{"generated", "", "", "no account exists; create the first admin", false},
		{"configured", "", supplied, "use the configured token", false},
		{"already issued", original, "", "setup token already issued", false},
		{"same supplied", original, original, "setup token already issued", false},
		{"mismatched", original, supplied, "configured setup token does not match", false},
		{"spent", original, supplied, "setup token was spent", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := dbtest.Open(t)
			s := store.New(d)
			if tt.initial != "" {
				if ok, err := s.IssueSetupToken(t.Context(), auth.HashToken(tt.initial)); !ok || err != nil {
					t.Fatalf("seed token: %v, %v", ok, err)
				}
			}
			if tt.spent {
				// Model an operator deleting every account after a previous setup.
				if _, err := d.ExecContext(t.Context(), `UPDATE setup SET consumed = true`); err != nil {
					t.Fatal(err)
				}
			}
			log, logs := webtest.Logger(t)
			if err := ensureSetupToken(t.Context(), s, log, tt.configured); err != nil {
				t.Fatal(err)
			}
			text := logs.String()
			webtest.NoSecrets(t, text, tt.message, supplied, original)
			var event struct {
				Token string `json:"setup_token"`
			}
			if err := json.Unmarshal([]byte(text), &event); err != nil {
				t.Fatal(err)
			}
			token := tt.initial
			if token == "" {
				token = tt.configured
			}
			if token == "" {
				token = event.Token
				if len(token) < 32 {
					t.Fatal("generated token missing from the first startup log")
				}
				nextLog, nextLogs := webtest.Logger(t)
				if err := ensureSetupToken(t.Context(), s, nextLog, ""); err != nil {
					t.Fatal(err)
				}
				webtest.NoSecrets(t, nextLogs.String(), "setup token already issued", token)
			} else if event.Token != "" {
				t.Fatal("startup disclosed an existing or supplied token")
			}
			state, err := s.SetupState(t.Context())
			if err != nil || state.TokenHash != auth.HashToken(token) || state.Consumed != tt.spent {
				t.Fatalf("wrong stored setup state: %v", err)
			}
		})
	}
}
