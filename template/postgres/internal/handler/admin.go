package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/jobs"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/middleware"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
	"github.com/17xande-dev/outbox"
)

func (s *site) usersPage(w http.ResponseWriter, r *http.Request) {
	s.renderUsers(w, r, http.StatusOK, "")
}

func (s *site) renderUsers(w http.ResponseWriter, r *http.Request, status int, message string) {
	ctx := r.Context()
	users, err := s.accounts.ListUsers(ctx)
	if err != nil {
		s.serverError(w, r, "user listing failed")
		return
	}
	// The count only decides what to show; the store enforces the guard.
	admins, err := s.accounts.EnabledAdminCount(ctx)
	if err != nil {
		s.serverError(w, r, "admin count failed")
		return
	}
	self, _ := middleware.User(r)
	s.render(w, r, status, "app/users", "Users", pageData{
		Message: message, Users: users, SelfID: self.ID, EnabledAdmins: admins,
		Roles: []auth.Role{auth.RoleMember, auth.RoleAdmin}, BackupLine: s.backupLine(),
		MailDelivers: s.mailDelivers, HasJobs: s.jobs != nil,
	})
}

func (s *site) backupLine() string {
	if s.newestBackup == nil {
		if s.noBackupsNote != "" {
			return s.noBackupsNote
		}
		return "Backups are turned off."
	}
	date, ok, err := s.newestBackup()
	switch {
	case err != nil:
		s.log.Error("backup lookup failed", "error", err)
		return "Backups could not be read."
	case !ok:
		return "No backups have been made yet."
	}
	return "Newest backup: " + date.Format("2006-01-02")
}

func parseRole(v string) (auth.Role, bool) {
	switch r := auth.Role(v); r {
	case auth.RoleAdmin, auth.RoleMember:
		return r, true
	}
	return "", false
}

func (s *site) userCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := strings.TrimSpace(r.PostForm.Get("name"))
	email, emailErr := auth.NormalizeEmail(r.PostForm.Get("email"))
	role, roleOK := parseRole(r.PostForm.Get("role"))
	plaintext := r.PostForm.Get("password")
	switch {
	case name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 120:
		s.renderUsers(w, r, http.StatusUnprocessableEntity, "Enter a name of at most 120 characters.")
		return
	case emailErr != nil:
		s.renderUsers(w, r, http.StatusUnprocessableEntity, "Enter a valid email address.")
		return
	case !roleOK:
		http.Error(w, "Unknown role.", http.StatusBadRequest)
		return
	case auth.ValidatePassword(plaintext) != nil:
		s.renderUsers(w, r, http.StatusUnprocessableEntity, "Use at least 12 characters for the temporary password.")
		return
	}
	hash, err := auth.HashPassword(ctx, plaintext)
	if err != nil {
		s.serverError(w, r, "temporary password hashing failed")
		return
	}
	_, err = s.accounts.CreateUser(ctx, auth.User{
		Name: name, Email: email, Role: role, PasswordHash: hash, MustChangePassword: true,
	})
	switch {
	case errors.Is(err, store.ErrEmailInUse):
		s.redirectNotice(w, r, "email-in-use")
	case err != nil:
		s.serverError(w, r, "account creation failed")
	default:
		s.redirectNotice(w, r, "user-created")
	}
}

// targetID treats anything other than a positive integer as not found.
func (s *site) targetID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.notFound(w, r)
		return 0, false
	}
	return id, true
}

// userChange maps the store's refusals to the page's outcomes.
func (s *site) userChange(w http.ResponseWriter, r *http.Request, err error, notice, failure string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r)
	case errors.Is(err, store.ErrLastAdmin):
		s.redirectNotice(w, r, "last-admin")
	case err != nil:
		s.serverError(w, r, failure)
	default:
		s.redirectNotice(w, r, notice)
	}
}

func (s *site) userRole(w http.ResponseWriter, r *http.Request) {
	id, ok := s.targetID(w, r)
	if !ok {
		return
	}
	role, ok := parseRole(r.PostForm.Get("role"))
	if !ok {
		http.Error(w, "Unknown role.", http.StatusBadRequest)
		return
	}
	s.userChange(w, r, s.accounts.SetRole(r.Context(), id, role), "role-changed", "role change failed")
}

func (s *site) userDisabled(w http.ResponseWriter, r *http.Request) {
	id, ok := s.targetID(w, r)
	if !ok {
		return
	}
	var disabled bool
	switch r.PostForm.Get("disabled") {
	case "1":
		disabled = true
	case "0":
	default:
		http.Error(w, "Unknown state.", http.StatusBadRequest)
		return
	}
	notice := "user-enabled"
	if disabled {
		notice = "user-disabled"
	}
	s.userChange(w, r, s.accounts.SetDisabled(r.Context(), id, disabled), notice, "disable change failed")
}

func (s *site) userPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := s.targetID(w, r)
	if !ok {
		return
	}
	plaintext := r.PostForm.Get("password")
	if auth.ValidatePassword(plaintext) != nil {
		s.renderUsers(w, r, http.StatusUnprocessableEntity, "Use at least 12 characters for the temporary password.")
		return
	}
	hash, err := auth.HashPassword(r.Context(), plaintext)
	if err != nil {
		s.serverError(w, r, "temporary password hashing failed")
		return
	}
	s.userChange(w, r, s.accounts.ResetPassword(r.Context(), id, hash), "password-reset", "password reset failed")
}

// mailTest queues one message to the signed-in admin to exercise delivery.
func (s *site) mailTest(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		http.Error(w, "Job queue is unavailable.", http.StatusServiceUnavailable)
		return
	}
	user, _ := middleware.User(r)
	_, _, err := s.jobs.EnqueueNow(r.Context(), outbox.Message{
		Kind: jobs.MailTest, Subject: "user:" + strconv.FormatInt(user.ID, 10), Payload: []byte(user.Email),
	})
	if err != nil {
		s.jobError(w, r, "test message enqueue failed", err)
		return
	}
	s.redirectNotice(w, r, "mail-queued")
}

func (s *site) retryJob(w http.ResponseWriter, r *http.Request) {
	id, ok := s.targetID(w, r)
	if !ok {
		return
	}
	if s.jobs == nil {
		http.Error(w, "Job queue is unavailable.", http.StatusServiceUnavailable)
		return
	}
	user, _ := middleware.User(r)
	retried, err := s.jobs.Retry(r.Context(), id, "user:"+strconv.FormatInt(user.ID, 10))
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case err != nil:
		s.jobError(w, r, "job retry failed", err)
	case !retried:
		http.Redirect(w, r, "/admin/jobs?notice=job-not-dead", http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/admin/jobs?notice=job-retried", http.StatusSeeOther)
	}
}

func (s *site) jobsPage(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		http.Error(w, "Job queue is unavailable.", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	stats, err := s.jobs.Stats(ctx)
	if err != nil {
		s.jobError(w, r, "job counts failed", err)
		return
	}
	dead, err := s.jobs.List(ctx, outbox.Filter{State: outbox.StateDead, Limit: 100})
	if err != nil {
		s.jobError(w, r, "dead job listing failed", err)
		return
	}
	s.render(w, r, http.StatusOK, "app/jobs", "Job queue", pageData{JobStats: stats, DeadJobs: dead})
}

func (s *site) jobError(w http.ResponseWriter, r *http.Request, message string, err error) {
	s.log.ErrorContext(r.Context(), message, "error_class", fmt.Sprintf("%T", err))
	s.render(w, r, http.StatusInternalServerError, "plain/server-error", "Something went wrong")
}

func (s *site) redirectNotice(w http.ResponseWriter, r *http.Request, code string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/admin/users?notice="+code, http.StatusSeeOther)
}
