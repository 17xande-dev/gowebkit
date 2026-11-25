package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
	"github.com/17xande-dev/websec/cookie"
	"github.com/17xande-dev/websec/session"
)

type userKey struct{}

// User reads the signed-in account installed by RequireUser.
func User(r *http.Request) (auth.User, bool) {
	user, ok := r.Context().Value(userKey{}).(auth.User)
	return user, ok
}

// RequireUser checks the opaque session before entering a protected route.
func RequireUser(accounts *store.Store, cookies *cookie.Cookie, life session.Lifetime, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			raw, ok := cookies.Value(r)
			if !ok {
				refuseAnonymous(w, r)
				return
			}
			digest := auth.HashToken(raw)
			user, stored, err := accounts.LookupSession(r.Context(), digest)
			if errors.Is(err, store.ErrNotFound) {
				refuseAnonymous(w, r)
				return
			}
			if err != nil {
				log.ErrorContext(r.Context(), "session lookup failed", "error", err)
				http.Error(w, "Session unavailable.", http.StatusInternalServerError)
				return
			}

			now := time.Now()
			if life.Expired(stored.CreatedAt, stored.LastSeen, now) {
				refuseAnonymous(w, r)
				return
			}
			if !life.IdleOff && session.NeedsTouch(stored.LastSeen, now) {
				if err := accounts.TouchSession(r.Context(), digest, now); err != nil {
					log.WarnContext(r.Context(), "session activity update failed", "error", err)
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
		})
	}
}

func refuseAnonymous(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		dest := "/login?" + url.Values{"next": {r.URL.RequestURI()}}.Encode()
		http.Redirect(w, r, dest, http.StatusSeeOther)
	default:
		http.Error(w, "Sign in to continue.", http.StatusUnauthorized)
	}
}
