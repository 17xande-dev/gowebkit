package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/dbtest"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
	"github.com/17xande-dev/websec/webtest"
)

// These tests hold sign-in behaviour that does not depend on the database
// engine. Anything engine-specific goes through a helper in database_test.go,
// which each database variant supplies for itself.

func TestSignInRehashAndConcurrentCredentialChange(t *testing.T) {
	const plaintext = "sample-passphrase-for-tests"
	const email = "legacy-account@example.com"
	const legacyHash = "$2b$05$ZmvX.UoZXP5vbcG9LpVV4.cM.SvL2FOMf0IquZmjOiZnJTjgfNuXa"
	for _, race := range []bool{false, true} {
		t.Run(fmt.Sprintf("concurrent_change=%t", race), func(t *testing.T) {
			setTestEnv(t, 8080)
			set := dbtest.Open(t)
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			accounts := store.New(set)
			if _, err := accounts.CreateUser(t.Context(), auth.User{
				Email: email, Name: "Legacy Account", PasswordHash: legacyHash, Role: auth.RoleMember,
			}); err != nil {
				t.Fatal(err)
			}
			if race {
				// Synchronously model a writer winning between credential
				// verification and rehash. The outer UPDATE affects zero rows.
				replacement, err := auth.HashPassword(t.Context(), "changed-sample-passphrase")
				if err != nil {
					t.Fatal(err)
				}
				winRehashRace(t, set, replacement)
			}
			log, logs := webtest.Logger(t)
			srv, err := newServer(cfg, log, accounts, set.Ping)
			if err != nil {
				t.Fatal(err)
			}
			c := webtest.New(t, srv.Handler, cfg.BaseURL)
			fields := url.Values{"email": {email}, "password": {plaintext}}
			if race {
				form := c.Page("/login").Form("/login")
				res := form.Set("email", email).Set("password", plaintext).Submit()
				res.WantStatus(http.StatusUnprocessableEntity)
				if !strings.Contains(res.Body, "Sign-in failed. Check your credentials and try again.") {
					t.Fatal("concurrent credential change did not return the generic refusal")
				}
				webtest.NoSecrets(t, logs.String(), `"msg":"sign-in failed"`, email, plaintext, legacyHash)
				// Losing the race is an expected refusal, not a server fault.
				if strings.Contains(logs.String(), "session creation failed") {
					t.Fatal("credential change race was logged as a server error")
				}
				// The competing credential survived; the losing sign-in
				// must not have made a usable session with the old one.
				c.Get("/").WantRedirect(http.StatusSeeOther, "/login?next=%2F")
				fields.Set("password", "changed-sample-passphrase")
			}
			res := c.SignIn("/login", fields)
			res.WantRedirect(http.StatusSeeOther, "/")
			c.Follow(res).WantStatus(http.StatusOK)
			got, err := accounts.UserByEmail(t.Context(), email)
			if err != nil || !strings.HasPrefix(got.PasswordHash, "$argon2id$") {
				t.Fatalf("stored credential was not upgraded: %v", err)
			}
		})
	}
}

func TestDisabledSignInChecksPasswordFirst(t *testing.T) {
	setTestEnv(t, 8080)
	d := dbtest.Open(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	const email = "disabled-account@example.com"
	const plaintext = "sample-passphrase-for-tests"
	hash, err := auth.HashPassword(t.Context(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	accounts := store.New(d)
	user, err := accounts.CreateUser(t.Context(), auth.User{
		Email: email, Name: "Disabled Account", PasswordHash: hash, Role: auth.RoleMember,
	})
	if err != nil {
		t.Fatal(err)
	}
	// An invalid stored hash lets the test observe whether password checking
	// ran without using a timing comparison between disabled and live users.
	if err := accounts.SetDisabled(t.Context(), user.ID, true); err != nil {
		t.Fatal(err)
	}
	corruptStoredHash(t, d, user.ID)
	log, logs := webtest.Logger(t)
	srv, err := newServer(cfg, log, accounts, d.Ping)
	if err != nil {
		t.Fatal(err)
	}
	c := webtest.New(t, srv.Handler, cfg.BaseURL)
	res := c.Page("/login").Form("/login").Set("email", email).Set("password", plaintext).Submit()
	res.WantStatus(http.StatusUnprocessableEntity)
	webtest.NoSecrets(t, logs.String(), "sign-in password check failed", email, plaintext)
	// The failure carries its cause, so an operator can tell it from an outage.
	if !strings.Contains(logs.String(), `"error":`) {
		t.Fatal("the password check failure was logged without its error")
	}
}
