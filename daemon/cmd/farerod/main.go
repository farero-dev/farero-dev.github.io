// farerod is the farero gateway daemon (아키텍처 2장). The app registers it
// as a LaunchAgent; it can also be run by hand for development:
//
//	FARERO_HOME=/tmp/fr FARERO_SOCKET=/tmp/fr/d.sock farerod --dev
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
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

// launchAgentLabel is the LaunchAgent label (LaunchAgent.swift in the app).
const launchAgentLabel = "dev.farero.farerod"

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
	log := slog.New(slog.NewTextHandler(logOutput(), &slog.HandlerOptions{Level: level}))
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
		Version:         version,
		Store:           st,
		Secrets:         secrets,
		Table:           tab,
		Log:             log,
		ApprovalTimeout: approvalTimeout(log, dev),
		Connectors:      plugins.Connectors(services),
	})
	if err != nil {
		return err
	}
	env.Core = c
	fixAgentPaths := registerAgentConfig(c, log, dev)

	// Open the socket before anything reads the Keychain: with ad-hoc
	// signing every update asks for Keychain access again, and until the
	// user answers, the app and the hooks still reach farerod.
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

	// Keychain-backed startup: plugin tokens, then the gateway secret.
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
	c.BroadcastSnapshot(ctx)
	go fixAgentPaths(ctx)
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Shutdown(sctx)
	log.Info("farerod stopped")
	return nil
}

// logOutput is stderr, or ~/Library/Logs/Farero/farerod.log when launchd
// runs farerod as the app's LaunchAgent (stderr goes nowhere there). The
// file is started over when it grows past 10 MB.
func logOutput() io.Writer {
	if os.Getenv("XPC_SERVICE_NAME") != launchAgentLabel {
		return os.Stderr
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return os.Stderr
	}
	dir := filepath.Join(home, "Library", "Logs", "Farero")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return os.Stderr
	}
	path := filepath.Join(dir, "farerod.log")
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fi, err := os.Stat(path); err == nil && fi.Size() > 10<<20 {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return os.Stderr
	}
	return f
}

// approvalTimeout is the approval deadline: 10 minutes (Q29), or
// FARERO_APPROVAL_TIMEOUT (a Go duration) in --dev, so tests need not wait
// that long.
func approvalTimeout(log *slog.Logger, dev bool) time.Duration {
	v := os.Getenv("FARERO_APPROVAL_TIMEOUT")
	if !dev || v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Warn("ignoring FARERO_APPROVAL_TIMEOUT", "value", v)
		return 0
	}
	log.Info("approval timeout", "timeout", d)
	return d
}

func secretStore(dev bool) secret.Store {
	if dev || os.Getenv("FARERO_SECRETS") == "file" {
		return secret.NewFileStore(paths.Secrets())
	}
	return newKeychain(keychainService)
}
