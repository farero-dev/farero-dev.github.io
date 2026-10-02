//go:build farero_dev

package main

import (
	"github.com/farero-dev/farero/daemon/internal/core"
	"github.com/farero-dev/farero/daemon/internal/policy"
	"github.com/farero-dev/farero/daemon/internal/upstream/devplugin"
)

// devRules adds the fake plugin's classification (development builds only).
func devRules(t *policy.Table) { t.AddPlugin(devplugin.Name, devplugin.Rules) }

// devPlugins connects the fake plugin.
func devPlugins(c *core.Core) { c.Plugins().Set(devplugin.New()) }
