package db

import "time"

// Connect and Provider let the external tests drive goose directly, which the
// public API deliberately does not allow (it refuses to roll back the baseline).
var (
	Connect  = connect
	Provider = provider

	ConnectionClass = connectionClass
)

// SetLockWait shortens the migration-lock wait for one test and returns the
// function that restores it.
func SetLockWait(d time.Duration) (restore func()) {
	old := lockWait
	lockWait = d
	return func() { lockWait = old }
}
