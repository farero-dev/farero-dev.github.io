// farerod is the farero gateway daemon (아키텍처 2장). The app registers it
// as a LaunchAgent; it can also be run by hand for development:
//
//	FARERO_HOME=/tmp/fr FARERO_SOCKET=/tmp/fr/d.sock farerod --dev
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/farero-dev/farero/daemon/internal/core"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/paths"
	"github.com/farero-dev/farero/daemon/internal/plugins"
	"github.com/farero-dev/farero/daemon/internal/policy"
	"github.com/farero-dev/farero/daemon/internal/secret"
	"github.com/farero-dev/farero/daemon/internal/store"
)

// version is set at build time.
var version = "dev"

// keychainService is the Keychain service name for farerod's items.
const keychainService = "dev.farero.farerod"

func main() {
	dev := flag.Bool("dev", false, "development mode: file secret store (and the fake plugin in farero_dev builds)")
	debug := flag.Bool("debug", false, "debug logging")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := run(log, *dev); err != nil {
		log.Error("farerod exiting", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, dev bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := paths.Ensure(); err != nil {
		return err
	}
	st, err := store.Open(paths.DB())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer st.Close()

	secrets := secretStore(dev)
	tab := policy.DefaultTable()
	if dev {
		devRules(tab)
	}
	env := &plugins.Env{Store: st, Secrets: secrets, Log: log, Version: version, GitHubClientID: githubClientID}
	services := plugins.Services(env)
	c, err := core.New(ctx, core.Options{
		Version:    version,
		Store:      st,
		Secrets:    secrets,
		Table:      tab,
		Log:        log,
		Connectors: plugins.Connectors(services),
	})
	if err != nil {
		return err
	}
	env.Core = c
	if dev {
		devPlugins(c)
	}
	plugins.RestoreAll(ctx, env, services)
	c.RefreshTools(ctx)
	if err := c.StartGateway(ctx); err != nil {
		// Keep running: the app shows the error and hooks still work.
		log.Error("gateway not started", "err", err)
	} else {
		log.Info("gateway listening", "url", c.GatewayInfo().URL)
	}
	registerAgentConfig(ctx, c, log, dev)

	l, err := ipc.Listen(paths.Socket())
	if err != nil {
		return err
	}
	defer os.Remove(paths.Socket())
	log.Info("farerod started", "version", version, "socket", paths.Socket(), "dev", dev)
	go c.SweepLoop(ctx)
	go func() {
		if err := ipc.Serve(ctx, l, c.Serve); err != nil {
			log.Error("ipc", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Shutdown(sctx)
	log.Info("farerod stopped")
	return nil
}

func secretStore(dev bool) secret.Store {
	if dev || os.Getenv("FARERO_SECRETS") == "file" {
		return secret.NewFileStore(paths.Secrets())
	}
	return newKeychain(keychainService)
}
