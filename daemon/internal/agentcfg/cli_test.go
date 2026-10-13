package agentcfg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NewClaude uses ~/.claude and ~/.claude.json, or CLAUDE_CONFIG_DIR for
// both like Claude Code does.
func TestNewClaudeLocations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	c := NewClaude("/hook", func() string { return "u" }, "/backups")
	if c.ConfigDir != filepath.Join(home, ".claude") || c.UserJSON != filepath.Join(home, ".claude.json") || c.BackupDir != "/backups" || c.HookPath != "/hook" {
		t.Fatalf("default: %+v", c)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/cfg")
	if c := NewClaude("/hook", nil, ""); c.ConfigDir != "/cfg" || c.UserJSON != "/cfg/.claude.json" {
		t.Fatalf("CLAUDE_CONFIG_DIR: %+v", c)
	}
}

func writeExec(t *testing.T, path, script string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// run returns stdout and stderr together, and the command's failure.
func TestRunCombinesOutput(t *testing.T) {
	dir := t.TempDir()
	writeExec(t, filepath.Join(dir, "fails"), "echo to-out; echo to-err >&2; exit 3")
	writeExec(t, filepath.Join(dir, "works"), `echo "args: $*"`)
	c := &Claude{}
	out, err := c.run(context.Background(), filepath.Join(dir, "fails"))
	if err == nil || !strings.Contains(string(out), "to-out") || !strings.Contains(string(out), "to-err") {
		t.Fatalf("fails: %q %v", out, err)
	}
	if out, err := c.run(context.Background(), filepath.Join(dir, "works"), "mcp", "list"); err != nil || strings.TrimSpace(string(out)) != "args: mcp list" {
		t.Fatalf("works: %q %v", out, err)
	}
}

// findCLI looks on PATH, then in the usual install locations under home,
// then asks the login shell; it remembers what it found while it exists.
func TestFindCLIOrder(t *testing.T) {
	for _, p := range []string{"/opt/homebrew/bin/claude", "/usr/local/bin/claude"} {
		if _, err := os.Stat(p); err == nil {
			t.Skipf("%s exists on this machine", p)
		}
	}
	dir := t.TempDir()
	home, bin := filepath.Join(dir, "home"), filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o755)
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("SHELL", "/usr/bin/false")
	ctx := context.Background()

	onPath := filepath.Join(bin, "claude")
	writeExec(t, onPath, "exit 0")
	c := &Claude{}
	if got := c.findCLI(ctx); got != onPath {
		t.Fatalf("on PATH: %q", got)
	}
	// Remembered while it exists, even when PATH changes.
	t.Setenv("PATH", t.TempDir())
	if got := c.findCLI(ctx); got != onPath {
		t.Fatalf("cached: %q", got)
	}
	os.Remove(onPath)

	// A directory in an install location is skipped; the next file counts.
	os.MkdirAll(filepath.Join(home, ".local/bin/claude"), 0o755)
	npm := filepath.Join(home, ".npm-global/bin/claude")
	writeExec(t, npm, "exit 0")
	if got := c.findCLI(ctx); got != npm {
		t.Fatalf("install location: %q", got)
	}
	os.Remove(npm)

	// The login shell knows it.
	shell := filepath.Join(dir, "shell")
	writeExec(t, shell, `[ "$1" = "-lc" ] && [ "$2" = "command -v claude" ] && echo "  /from/shell/claude  "`)
	t.Setenv("SHELL", shell)
	if got := c.findCLI(ctx); got != "/from/shell/claude" {
		t.Fatalf("login shell: %q", got)
	}

	// Nowhere.
	t.Setenv("SHELL", "/usr/bin/false")
	if got := c.findCLI(ctx); got != "" {
		t.Fatalf("nowhere: %q", got)
	}

	// The configured CLI wins over everything.
	if got := (&Claude{CLI: "/configured/claude"}).findCLI(ctx); got != "/configured/claude" {
		t.Fatalf("configured: %q", got)
	}
}

// Without a claude executable, Plan still shows the commands, naming plain
// "claude"; with one, it names that path.
func TestPlanNamesTheCLI(t *testing.T) {
	for _, p := range []string{"/opt/homebrew/bin/claude", "/usr/local/bin/claude"} {
		if _, err := os.Stat(p); err == nil {
			t.Skipf("%s exists on this machine", p)
		}
	}
	c, _ := setup(t, "")
	c.CLI = ""
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", "/usr/bin/false")
	p, err := c.Plan(context.Background())
	if err != nil || len(p.Commands) != 2 || !strings.HasPrefix(p.Commands[0], "claude mcp remove farero") || !strings.HasPrefix(p.Commands[1], "claude mcp add-json farero ") {
		t.Fatalf("plan: %+v %v", p.Commands, err)
	}
	c, _ = setup(t, "")
	if p, err := c.Plan(context.Background()); err != nil || !strings.HasPrefix(p.Commands[0], "/usr/local/bin/claude mcp remove") {
		t.Fatalf("configured CLI: %+v %v", p.Commands, err)
	}
}
