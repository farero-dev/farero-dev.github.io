//go:build !(darwin && cgo)

package main

import (
	"github.com/farero-dev/farero/daemon/internal/paths"
	"github.com/farero-dev/farero/daemon/internal/secret"
)

// Without cgo there is no Keychain access; fall back to the file store.
func newKeychain(string) secret.Store { return secret.NewFileStore(paths.Secrets()) }
