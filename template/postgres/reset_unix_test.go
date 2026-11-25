//go:build unix

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Opening a named pipe blocks until something writes to it, so the file must be
// checked before it is opened.
func TestResetPasswordRefusesAPipeWithoutBlocking(t *testing.T) {
	resetFixture(t)
	pipe := filepath.Join(t.TempDir(), "password")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Skipf("cannot make a named pipe here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- execute([]string{"-reset-password", "operator@example.com", "-password-file", pipe},
			strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("error = %v, want a regular-file refusal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the command blocked opening a named pipe")
	}
}
