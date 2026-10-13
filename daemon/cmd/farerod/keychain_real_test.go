//go:build darwin && cgo

package main

// keychainIsReal: newKeychain returns the Keychain store here. Tests only
// check its type; they never call it (ad-hoc signing makes every access
// prompt).
const keychainIsReal = true
