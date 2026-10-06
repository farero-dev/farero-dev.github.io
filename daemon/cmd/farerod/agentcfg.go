package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/farero-dev/farero/daemon/internal/agentcfg"
	"github.com/farero-dev/farero/daemon/internal/core"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/paths"
)

// registerAgentConfig wires the Claude Code installer (F-06) into the UI
// protocol. It must run before the socket serves. The returned function
// fixes stale paths after the app moved (Q63); it needs the gateway URL, so
// farerod calls it once the gateway started.
//
// FARERO_CLAUDE_CONFIG_DIR points the installer (and the claude CLI it runs)
// at another Claude Code config directory, so development runs never touch
// the developer's real ~/.claude. In --dev mode without it, the automatic
// path fix is skipped.
func registerAgentConfig(c *core.Core, log *slog.Logger, dev bool) (fixPath func(context.Context)) {
	exe, err := os.Executable()
	if err != nil {
		log.Error("agent config disabled", "err", err)
		return func(context.Context) {}
	}
	exe, _ = filepath.EvalSymlinks(exe)
	hook := filepath.Join(filepath.Dir(exe), "farero-hook")
	isolated := false
	if d := os.Getenv("FARERO_CLAUDE_CONFIG_DIR"); d != "" {
		os.Setenv("CLAUDE_CONFIG_DIR", d) // inherited by the claude CLI
		isolated = true
	}
	claude := agentcfg.NewClaude(hook, func() string { return c.GatewayInfo().URL }, paths.Backups())

	check := func(m ipc.Message) error {
		r, err := ipc.Decode[ipc.AgentRef](m)
		if err != nil {
			return err
		}
		if r.Agent != "" && r.Agent != "claude" {
			return fmt.Errorf("unsupported agent %q", r.Agent)
		}
		return nil
	}
	c.HandleUI(ipc.TypeAgentCfgStatus, func(ctx context.Context, m ipc.Message) (string, any, error) {
		if err := check(m); err != nil {
			return "", nil, err
		}
		return ipc.TypeAgentCfgStatus, claude.Status(ctx), nil
	})
	c.HandleUI(ipc.TypeAgentCfgPlan, func(ctx context.Context, m ipc.Message) (string, any, error) {
		if err := check(m); err != nil {
			return "", nil, err
		}
		p, err := claude.Plan(ctx)
		return ipc.TypeAgentCfgPlan, p, err
	})
	c.HandleUI(ipc.TypeAgentCfgApply, func(ctx context.Context, m ipc.Message) (string, any, error) {
		if err := check(m); err != nil {
			return "", nil, err
		}
		if gi := c.GatewayInfo(); gi.URL == "" {
			if gi.Error != "" {
				return "", nil, fmt.Errorf("게이트웨이가 실행 중이 아님: %s", gi.Error)
			}
			return "", nil, fmt.Errorf("게이트웨이가 아직 시작되지 않음: 키체인 접근 허용을 기다리는 중일 수 있음")
		}
		st, err := claude.Apply(ctx)
		return ipc.TypeAgentCfgStatus, st, err
	})
	c.HandleUI(ipc.TypeAgentCfgRemove, func(ctx context.Context, m ipc.Message) (string, any, error) {
		if err := check(m); err != nil {
			return "", nil, err
		}
		st, err := claude.Remove(ctx)
		return ipc.TypeAgentCfgStatus, st, err
	})

	return func(ctx context.Context) {
		if c.GatewayInfo().URL == "" || (dev && !isolated) {
			return
		}
		if fixed, err := claude.FixPath(ctx); err != nil {
			log.Error("fix agent config path", "err", err)
		} else if fixed {
			log.Info("agent config paths updated", "hook", hook)
		}
	}
}
