// Package applog optionally redirects the standard logger to a file.
//
// Needed because the autostart scripts build the binaries with
// -ldflags="-H=windowsgui" so no console window appears at login — but a
// windowsgui binary has no console to write log.Printf output to, so
// without this it would just vanish. Interactive `go run` usage is
// unaffected: without LOG_FILE set, logs still go to stderr as normal.
package applog

import (
	"log"
	"os"
)

// Setup redirects the standard logger to path. If path is empty, it falls
// back to the LOG_FILE env var; if that's empty too, logging is left on
// stderr. Returns a close function to defer in main(); safe to call even
// when no path is configured (returns a no-op).
//
// Taking path as a parameter (rather than only reading LOG_FILE) lets the
// bot and agent — which usually share one .env when running on the same
// machine — each get their own log file via a per-task CLI flag instead of
// fighting over one shared env var.
func Setup(path string) func() {
	if path == "" {
		path = os.Getenv("LOG_FILE")
	}
	if path == "" {
		return func() {}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("applog: could not open %s, keeping default output: %v", path, err)
		return func() {}
	}
	log.SetOutput(f)
	return func() { f.Close() }
}
