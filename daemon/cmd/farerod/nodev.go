//go:build !farero_dev

package main

import (
	"github.com/farero-dev/farero/daemon/internal/core"
	"github.com/farero-dev/farero/daemon/internal/policy"
)

// Release builds have no fake plugin (Q67).
func devRules(*policy.Table) {}
func devPlugins(*core.Core)  {}
