package main

import (
	"context"
	"log/slog"

	"github.com/farero-dev/farero/daemon/internal/core"
	"github.com/farero-dev/farero/daemon/internal/secret"
	"github.com/farero-dev/farero/daemon/internal/store"
)

// connectors returns the plugin connectors (filled in by M5).
func connectors(log *slog.Logger, st *store.Store, secrets secret.Store) map[string]core.Connector {
	return map[string]core.Connector{}
}

// restorePlugins reconnects plugins that were connected before a restart.
func restorePlugins(ctx context.Context, log *slog.Logger, c *core.Core) {}
