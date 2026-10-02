//go:build darwin && cgo

package main

import "github.com/farero-dev/farero/daemon/internal/secret"

func newKeychain(service string) secret.Store { return secret.NewKeychain(service) }
