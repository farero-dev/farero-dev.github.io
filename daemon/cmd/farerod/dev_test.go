//go:build farero_dev

package main

import (
	"strings"
	"testing"

	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/policy"
	"github.com/farero-dev/farero/daemon/internal/upstream/devplugin"
)

func TestDevRulesAddFakePlugin(t *testing.T) {
	tab := policy.DefaultTable()
	if _, ok := tab.Plugins[devplugin.Name]; ok {
		t.Fatal("default table must not carry the dev plugin")
	}
	devRules(tab)
	if got := len(tab.Plugins[devplugin.Name].Tools); got != len(devplugin.Rules) {
		t.Fatalf("dev rules = %d, want %d", got, len(devplugin.Rules))
	}
}

func TestRunConnectsDevPlugin(t *testing.T) {
	sb := newSandbox(t)
	d := sb.start(t, true)
	conn, _ := d.waitGateway()
	defer conn.Close()
	m := call(t, conn, ipc.TypePluginTools, ipc.PluginRef{Plugin: "dev"})
	if m.Type == ipc.TypeError {
		t.Fatalf("plugin.tools: %s", m.Data)
	}
	if !strings.Contains(string(m.Data), "echo") {
		t.Errorf("dev tools not listed: %s", m.Data)
	}
}
