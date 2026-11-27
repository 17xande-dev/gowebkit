package handler

import (
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/auth"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/store"
	"github.com/17xande-dev/password"
	"github.com/17xande-dev/ratelimit"
	"github.com/17xande-dev/websec"
	"github.com/17xande-dev/websec/csrf"
)

// checkDummy is a variable so a test can see that an unknown account still pays
// for a password check, which a timing comparison cannot show reliably.
var checkDummy = password.CheckDummy

const signInFailed = "Sign-in failed. Check your credentials and try again."

func loginDestination(next string) string {
	return websec.SafeRedirect(next, websec.RedirectOptions{Deny: []string{"/login", "/logout", "/setup"}, Fallback: "/"})
}

func (s *site) loginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "plain/login", "Sign in", pageData{Next: loginDestination(r.URL.Query().Get("next"))})
}

func (s *site) login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	next := r.PostForm.Get("next")
	if !r.PostForm.Has("next") {
		next = r.URL.Query().Get("next")
	}
	dest := loginDestination(next)
	input, plaintext := r.PostForm.Get("email"), r.PostForm.Get("password")
	email, emailErr := auth.NormalizeEmail(input)
	key := email
	if emailErr != nil {
		key = input
	}
	if !s.accountLimit.Allow(ratelimit.AccountKey(key)) {
		rateRefusal(w, r)
		return
	}

	failure := func(user auth.User) {
		ip := s.clientIP.Addr(r).String()
		if user.ID == 0 {
			s.log.WarnContext(ctx, "sign-in failed: unknown account", "client_ip", ip)
		} else {
			s.log.WarnContext(ctx, "sign-in failed", "user_id", user.ID, "client_ip", ip)
		}
		s.render(w, r, http.StatusUnprocessableEntity, "plain/login", "Sign in", pageData{Message: signInFailed, Next: dest, Email: input})
	}
	var user auth.User
	var err error
	if emailErr == nil {
		user, err = s.accounts.UserByEmail(ctx, email)
	}
	if emailErr != nil || errors.Is(err, store.ErrNotFound) {
		if _, err := checkDummy(ctx, plaintext); err != nil {
			s.log.WarnContext(ctx, "dummy password check failed")
		}
		failure(auth.User{})
		return
	}
	if err != nil {
		s.log.ErrorContext(ctx, "sign-in account lookup failed", "error", err)
		failure(auth.User{})
		return
	}
	ok, rehash, err := password.Check(ctx, user.PasswordHash, plaintext)
	if err != nil {
		s.log.ErrorContext(ctx, "sign-in password check failed", "user_id", user.ID, "error", err)
	}
	if err != nil || !ok || user.Disabled {
		failure(user)
		return
	}

	if rehash {
		newHash, err := password.Hash(ctx, plaintext)
		if err != nil {
			s.log.ErrorContext(ctx, "sign-in password rehash failed", "user_id", user.ID, "error", err)
			failure(user)
			return
		}
		replaced, err := s.accounts.RehashPassword(ctx, user.ID, user.PasswordHash, newHash)
		if err != nil {
			s.log.ErrorContext(ctx, "sign-in password rehash write failed", "user_id", user.ID, "error", err)
			failure(user)
			return
		}
		if replaced {
			// Session creation guards on the current hash, not the one we read.
			user.PasswordHash = newHash
		}
	}

	if err := s.startSession(w, r, user); err != nil {
		if !errors.Is(err, store.ErrCredentialsChanged) {
			s.log.ErrorContext(ctx, "sign-in session creation failed", "user_id", user.ID, "error", err)
		}
		failure(user)
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (s *site) startSession(w http.ResponseWriter, r *http.Request, user auth.User) error {
	token := auth.NewToken()
	hash := auth.HashToken(token)
	now := time.Now()
	if err := s.accounts.CreateSession(r.Context(), user.ID, user.PasswordHash, hash, now); err != nil {
		return err
	}
	// Signing in over a live session must end the one the browser presented:
	// on a shared computer it would otherwise stay valid until it expires.
	if old, ok := s.cookies.Value(r); ok {
		if err := s.accounts.DeleteSession(r.Context(), auth.HashToken(old)); err != nil {
			return errors.Join(err, s.accounts.DeleteSession(r.Context(), hash))
		}
	}
	if _, err := s.csrf.Renew(w, r); err != nil {
		return errors.Join(err, s.accounts.DeleteSession(r.Context(), hash))
	}
	s.cookies.Set(w, token, s.life.AbsoluteExpiry(now))
	w.Header().Set("Cache-Control", "no-store")
	return nil
}

func (s *site) logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if raw, ok := s.cookies.Value(r); ok {
		if err := s.accounts.DeleteSession(r.Context(), auth.HashToken(raw)); err != nil {
			s.serverError(w, r, "sign-out session deletion failed")
			return
		}
	}
	if _, err := s.csrf.Renew(w, r); err != nil {
		s.serverError(w, r, "sign-out token renewal failed")
		return
	}
	s.cookies.Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func rateRefusal(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "Too many attempts. Try again later.", http.StatusTooManyRequests)
}

func (s *site) serverError(w http.ResponseWriter, r *http.Request, message string) {
	s.log.ErrorContext(r.Context(), message)
	s.render(w, r, http.StatusInternalServerError, "plain/server-error", "Something went wrong")
}

func csrfBodyError(w http.ResponseWriter, r *http.Request, status int) {
	switch status {
	case http.StatusRequestEntityTooLarge:
		http.Error(w, "That submission is too large.", status)
	case http.StatusUnsupportedMediaType:
		http.Error(w, "Submit the form without attachments.", status)
	default:
		if errors.Is(csrf.BodyError(r), os.ErrDeadlineExceeded) {
			w.Header().Set("Connection", "close")
			http.Error(w, "That submission took too long. Try again.", http.StatusRequestTimeout)
			return
		}
		http.Error(w, "That submission could not be read.", status)
	}
}
