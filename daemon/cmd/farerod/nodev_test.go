//go:build !farero_dev

package main

import (
	"testing"

	"github.com/farero-dev/farero/daemon/internal/policy"
)

// Release builds have no fake plugin (Q67).
func TestDevHooksAreNoOpsInReleaseBuilds(t *testing.T) {
	tab := policy.DefaultTable()
	n := len(tab.Plugins)
	devRules(tab)
	if len(tab.Plugins) != n {
		t.Fatal("devRules changed the table in a release build")
	}
	if _, ok := tab.Plugins["dev"]; ok {
		t.Fatal("dev plugin in release table")
	}
	devPlugins(nil) // must not touch the core
}
