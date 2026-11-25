package handler

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
)

func (s *site) onlyWithoutUsers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count, err := s.accounts.UserCount(r.Context())
		if err != nil {
			s.serverError(w, r, "setup account count failed")
			return
		}
		if count != 0 {
			s.notFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *site) setupPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "plain/setup", "Set up account")
}

func (s *site) setup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	refuse := func(message string) {
		s.render(w, r, http.StatusUnprocessableEntity, "plain/setup", "Set up account", pageData{Message: message})
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	email, err := auth.NormalizeEmail(r.PostForm.Get("email"))
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 120 {
		refuse("Enter a name of at most 120 characters.")
		return
	}
	if err != nil {
		refuse("Enter a valid email address.")
		return
	}
	plaintext := r.PostForm.Get("password")
	if err := auth.ValidatePassword(plaintext); err != nil {
		refuse("Use at least 12 characters for the password.")
		return
	}

	digest := auth.HashToken(r.PostForm.Get("token"))
	state, err := s.accounts.SetupState(ctx)
	if errors.Is(err, store.ErrNotFound) || err == nil && (state.Consumed ||
		subtle.ConstantTimeCompare(state.TokenHash[:], digest[:]) != 1) {
		// The one public endpoint that guesses a secret. The token itself is never logged.
		s.log.WarnContext(ctx, "setup refused: token invalid or unavailable", "client_ip", s.clientIP.Addr(r).String())
		refuse("The setup token is invalid or no longer available.")
		return
	}
	if err != nil {
		s.serverError(w, r, "setup token lookup failed")
		return
	}
	hash, err := auth.HashPassword(ctx, plaintext)
	if err != nil {
		s.serverError(w, r, "setup password hashing failed")
		return
	}
	user, err := s.accounts.ClaimSetup(ctx, digest, auth.User{Name: name, Email: email, PasswordHash: hash})
	if errors.Is(err, store.ErrSetupUnavailable) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, "first account creation failed")
		return
	}
	if err := s.startSession(w, r, user); err != nil {
		// The account has been created. The token stays spent even if the
		// automatic sign-in fails; the new password still works at /login.
		s.log.ErrorContext(ctx, "first account session creation failed", "user_id", user.ID)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
