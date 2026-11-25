package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/auth"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/config"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/db"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/store"
)

// maxPasswordInput bounds what the operator command reads; no real password
// comes near it.
const maxPasswordInput = 1 << 10

// resetPassword is the operator's way back in, run from the one binary because
// the production image has no shell. The password is installed as a temporary
// one: the account must change it at its next sign-in, which also means the
// operator's knowledge of it ends there. Its sessions end at once.
func resetPassword(ctx context.Context, cfg config.Config, email, file string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	normalized, err := auth.NormalizeEmail(email)
	if err != nil {
		return err
	}
	// Opening would migrate an empty database and then report a missing
	// account, so look for migration history without creating any.
	exists, err := db.Exists(ctx, cfg.DB)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("the database has not been migrated; run the server or -migrate first")
	}
	plaintext, err := readNewPassword(file, stdin, stderr)
	if err != nil {
		return err
	}
	// Same policy as setup, admin create and password change.
	if err := auth.ValidatePassword(plaintext); err != nil {
		return err
	}
	setPasswordConcurrency(cfg.PasswordConcurrency)
	d, err := db.Open(ctx, cfg.DB)
	if err != nil {
		return err
	}
	// err is the named result, so a failed close is reported, not dropped.
	defer func() { err = errors.Join(err, d.Close()) }()
	accounts := store.New(d)
	user, err := accounts.UserByEmail(ctx, normalized)
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("no account has that email address")
	}
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(ctx, plaintext)
	if err != nil {
		return err
	}
	if err := accounts.ResetPassword(ctx, user.ID, hash); err != nil {
		return err
	}
	if user.Disabled {
		_, err = fmt.Fprintln(stdout, "Password set and sessions ended, but the account is disabled and cannot sign in; enable it from the Users page")
		return err
	}
	_, err = fmt.Fprintln(stdout, "Password set; the account must choose a new one at its next sign-in, and its sessions were ended")
	return err
}

// readNewPassword takes the password from a file when named, else from one
// line of standard input, with a prompt on stderr (a pipe just ignores it).
// The terminal echoes what is typed; the docs recommend a pipe or a file.
func readNewPassword(file string, stdin io.Reader, stderr io.Writer) (string, error) {
	var src io.Reader = stdin
	if file != "" {
		// Look before opening: opening a FIFO would block until a writer appears.
		if info, err := os.Stat(file); err != nil || !info.Mode().IsRegular() {
			return "", errors.New("the password file must be a regular file")
		}
		f, err := os.Open(file)
		if err != nil {
			return "", errors.New("could not open the password file")
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return "", errors.New("the password file must be a regular file")
		}
		src = f
	} else {
		fmt.Fprint(stderr, "New password (one line on standard input): ")
	}
	line, err := bufio.NewReader(io.LimitReader(src, maxPasswordInput+1)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("could not read the password")
	}
	if len(line) > maxPasswordInput {
		return "", errors.New("the password input is too long")
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if line == "" {
		return "", errors.New("no password was provided")
	}
	return line, nil
}
