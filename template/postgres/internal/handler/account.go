package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/middleware"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
	"github.com/17xande-dev/password"
	"github.com/17xande-dev/ratelimit"
)

func (s *site) passwordPage(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.User(r)
	s.render(w, r, http.StatusOK, "app/password", "Change password", pageData{MustChange: user.MustChangePassword})
}

// passwordChange needs the current password even though the session is valid:
// a borrowed session must not be enough to lock the owner out.
func (s *site) passwordChange(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, _ := middleware.User(r)
	// The per-address tier is not enough here: a stolen session used from many
	// addresses could otherwise guess the current password without limit. The
	// "user:" prefix cannot collide with a sign-in key, which is an email address.
	if !s.accountLimit.Allow(ratelimit.AccountKey("user:" + strconv.FormatInt(user.ID, 10))) {
		rateRefusal(w, r)
		return
	}
	refuse := func(message string) {
		s.render(w, r, http.StatusUnprocessableEntity, "app/password", "Change password",
			pageData{Message: message, MustChange: user.MustChangePassword})
	}
	current, next := r.PostForm.Get("current"), r.PostForm.Get("new")
	ok, _, err := password.Check(ctx, user.PasswordHash, current)
	if err != nil {
		s.log.ErrorContext(ctx, "password change check failed", "user_id", user.ID)
		refuse("The current password could not be checked. Try again.")
		return
	}
	if !ok {
		s.log.WarnContext(ctx, "password change refused: wrong current password", "user_id", user.ID,
			"client_ip", s.clientIP.Addr(r).String())
		refuse("The current password is not correct.")
		return
	}
	switch {
	case next != r.PostForm.Get("confirm"):
		refuse("The new password and its confirmation differ.")
		return
	case auth.ValidatePassword(next) != nil:
		refuse("Use at least 12 characters for the new password.")
		return
	case next == current:
		refuse("Choose a password different from the current one.")
		return
	}
	hash, err := auth.HashPassword(ctx, next)
	if err != nil {
		s.serverError(w, r, "password change hashing failed")
		return
	}
	raw, _ := s.cookies.Value(r)
	err = s.accounts.ChangePassword(ctx, user.ID, user.PasswordHash, hash, auth.HashToken(raw))
	if errors.Is(err, store.ErrCredentialsChanged) {
		refuse("Your account changed while you were typing. Try again.")
		return
	}
	if err != nil {
		s.serverError(w, r, "password change failed")
		return
	}
	// The old form token must not outlive the credential change.
	if _, err := s.csrf.Renew(w, r); err != nil {
		s.serverError(w, r, "password change token renewal failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/?notice=password-changed", http.StatusSeeOther)
}
