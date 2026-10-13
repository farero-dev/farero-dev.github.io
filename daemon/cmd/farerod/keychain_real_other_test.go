//go:build !(darwin && cgo)

package main

// Without cgo newKeychain falls back to the file store.
const keychainIsReal = false
