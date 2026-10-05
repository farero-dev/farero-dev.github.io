// Package paths resolves the on-disk locations farero uses.
//
// Everything lives under ~/Library/Application Support/Farero. FARERO_HOME
// overrides that directory and FARERO_SOCKET overrides the socket path, which
// development runs and tests use to stay away from the real install (a Unix
// socket path must stay under 104 bytes on macOS, so tests point
// FARERO_SOCKET at a short path in /tmp).
package paths

import (
	"os"
	"path/filepath"
)

// SupportDir returns the farero data directory, creating nothing.
func SupportDir() string {
	if d := os.Getenv("FARERO_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, "Library", "Application Support", "Farero")
}

// Socket returns the farerod Unix socket path.
func Socket() string {
	if s := os.Getenv("FARERO_SOCKET"); s != "" {
		return s
	}
	return filepath.Join(SupportDir(), "farerod.sock")
}

// DB returns the SQLite database path.
func DB() string { return filepath.Join(SupportDir(), "farero.db") }

// Config returns the daemon config file path (gateway port and similar).
func Config() string { return filepath.Join(SupportDir(), "config.json") }

// Backups returns the directory for agent config backups.
func Backups() string { return filepath.Join(SupportDir(), "backups") }

// Secrets returns the file used by the development secret store.
func Secrets() string { return filepath.Join(SupportDir(), "dev-secrets.json") }

// Ensure creates the support directory with user-only permissions.
func Ensure() error { return os.MkdirAll(SupportDir(), 0o700) }
