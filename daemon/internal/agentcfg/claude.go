// Package agentcfg registers farero with coding agents (F-06): the hook
// bridge for every hook event, the gateway MCP server, and the rule that
// auto-allows gateway tools in the agent so approval happens only in farero
// (Q5). MVP supports Claude Code.
package agentcfg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/farero-dev/farero/daemon/internal/ipc"
)

// MinClaudeVersion is the oldest Claude Code farero supports: the per-server
// timeout only extends the idle timeout from v2.1.203 (Q53).
const MinClaudeVersion = "2.1.203"

// ServerName is the gateway's MCP server name (Q65).
const ServerName = "farero"

// AllowRule auto-allows every gateway tool in Claude Code.
const AllowRule = "mcp__farero__*"

// HookTimeout lets a PermissionRequest wait the full 10 minute approval
// window (Q52). Verified in M0: a hook waited 620 s with this setting.
const HookTimeout = 660

// ServerTimeoutMS is the gateway server's per-server tool timeout. Without
// it Claude Code gives up on a call after about 60 s (M0).
const ServerTimeoutMS = 660000

// hookEvents are the Claude Code events farero listens to (기능 명세서 F-02).
var hookEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure",
	"PermissionRequest", "Notification", "Stop", "SessionEnd",
}

// toolEvents take a matcher.
var toolEvents = map[string]bool{"PreToolUse": true, "PostToolUse": true, "PostToolUseFailure": true, "PermissionRequest": true}

// hookMarker identifies farero's hook commands regardless of install path.
const hookMarker = "farero-hook"

// Claude installs farero into Claude Code's user configuration.
type Claude struct {
	HookPath   string        // absolute path of farero-hook
	GatewayURL func() string // current gateway URL
	ConfigDir  string        // ~/.claude (settings.json lives here)
	UserJSON   string        // ~/.claude.json (user-scope MCP servers)
	BackupDir  string
	// CLI overrides discovery of the claude executable (tests).
	CLI string
	// Run executes a command (tests replace it).
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)

	mu      sync.Mutex
	notice  string
	cliPath string
}

// NewClaude returns an installer using the standard locations, honouring
// CLAUDE_CONFIG_DIR like Claude Code does.
func NewClaude(hookPath string, gatewayURL func() string, backupDir string) *Claude {
	home, _ := os.UserHomeDir()
	cfg := filepath.Join(home, ".claude")
	user := filepath.Join(home, ".claude.json")
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		cfg = d
		user = filepath.Join(d, ".claude.json")
	}
	return &Claude{HookPath: hookPath, GatewayURL: gatewayURL, ConfigDir: cfg, UserJSON: user, BackupDir: backupDir}
}

func (c *Claude) settingsPath() string { return filepath.Join(c.ConfigDir, "settings.json") }

func (c *Claude) hookCommand() string {
	return strconv.Quote(c.HookPath) + " --agent claude"
}

func (c *Claude) headersCommand() string {
	return strconv.Quote(c.HookPath) + " --headers --agent claude"
}

func (c *Claude) serverJSON() string {
	b, _ := marshal(map[string]any{
		"type":          "http",
		"url":           c.GatewayURL(),
		"headersHelper": c.headersCommand(),
		"timeout":       ServerTimeoutMS,
	})
	return string(b)
}

func (c *Claude) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.Run != nil {
		return c.Run(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// findCLI locates the claude executable. A LaunchAgent has a minimal PATH,
// so common install locations and the user's login shell are tried too.
func (c *Claude) findCLI(ctx context.Context) string {
	if c.CLI != "" {
		return c.CLI
	}
	c.mu.Lock()
	cached := c.cliPath
	c.mu.Unlock()
	if cached != "" {
		if _, err := os.Stat(cached); err == nil {
			return cached
		}
	}
	found := ""
	if p, err := exec.LookPath("claude"); err == nil {
		found = p
	}
	if found == "" {
		home, _ := os.UserHomeDir()
		for _, p := range []string{
			filepath.Join(home, ".local/bin/claude"),
			filepath.Join(home, ".claude/local/claude"),
			"/opt/homebrew/bin/claude",
			"/usr/local/bin/claude",
			filepath.Join(home, ".npm-global/bin/claude"),
			filepath.Join(home, ".bun/bin/claude"),
		} {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				found = p
				break
			}
		}
	}
	if found == "" {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		out, err := exec.CommandContext(sctx, shell, "-lc", "command -v claude").Output()
		cancel()
		if err == nil {
			found = strings.TrimSpace(string(out))
		}
	}
	c.mu.Lock()
	c.cliPath = found
	c.mu.Unlock()
	return found
}

// Status inspects the current configuration.
func (c *Claude) Status(ctx context.Context) ipc.AgentCfgStatus {
	st := ipc.AgentCfgStatus{
		Agent: "claude", MinVersion: MinClaudeVersion, SettingsPath: c.settingsPath(),
		HookPath: c.HookPath, GatewayURL: c.GatewayURL(),
	}
	c.mu.Lock()
	st.Message = c.notice
	c.mu.Unlock()
	if cli := c.findCLI(ctx); cli != "" {
		st.CLIFound = true
		st.CLIPath = cli
		if out, err := c.run(ctx, cli, "--version"); err == nil {
			st.Version = parseVersion(string(out))
			st.VersionOK = versionAtLeast(st.Version, MinClaudeVersion)
		}
	}
	settings, err := c.readSettings()
	if err == nil {
		st.HooksInstalled, st.StalePath = c.hooksState(settings)
		st.AllowInstalled = hasAllow(settings)
	}
	if srv, ok := c.installedServer(); ok {
		st.MCPInstalled = true
		if srv.URL != c.GatewayURL() || srv.HeadersHelper != c.headersCommand() || srv.Timeout != ServerTimeoutMS {
			st.StalePath = true
		}
	}
	return st
}

type serverEntry struct {
	Type          string `json:"type"`
	URL           string `json:"url"`
	HeadersHelper string `json:"headersHelper"`
	Timeout       int    `json:"timeout"`
}

// installedServer reads (never writes) the user-scope MCP server entry.
func (c *Claude) installedServer() (serverEntry, bool) {
	b, err := os.ReadFile(c.UserJSON)
	if err != nil {
		return serverEntry{}, false
	}
	var doc struct {
		MCPServers map[string]serverEntry `json:"mcpServers"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return serverEntry{}, false
	}
	s, ok := doc.MCPServers[ServerName]
	return s, ok
}

func (c *Claude) readSettings() (*object, error) {
	b, err := os.ReadFile(c.settingsPath())
	if errors.Is(err, os.ErrNotExist) {
		return newObject(), nil
	}
	if err != nil {
		return nil, err
	}
	return parseObject(b)
}

// hookEntry is the part of a matcher entry farero inspects.
type hookEntry struct {
	Matcher string `json:"matcher,omitempty"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	} `json:"hooks"`
}

func (c *Claude) hooksState(settings *object) (installed, stale bool) {
	hooks, err := settings.child("hooks")
	if err != nil {
		return false, false
	}
	installed = true
	for _, ev := range hookEvents {
		raw, ok := hooks.get(ev)
		if !ok {
			installed = false
			continue
		}
		var entries []hookEntry
		if json.Unmarshal(raw, &entries) != nil {
			installed = false
			continue
		}
		found := false
		for _, e := range entries {
			for _, h := range e.Hooks {
				if strings.Contains(h.Command, hookMarker) {
					found = true
					if h.Command != c.hookCommand() {
						stale = true
					}
				}
			}
		}
		if !found {
			installed = false
		}
	}
	return installed, stale
}

func hasAllow(settings *object) bool {
	perms, err := settings.child("permissions")
	if err != nil {
		return false
	}
	raw, ok := perms.get("allow")
	if !ok {
		return false
	}
	var allow []string
	json.Unmarshal(raw, &allow)
	for _, a := range allow {
		if a == AllowRule {
			return true
		}
	}
	return false
}

// rewriteHooks removes farero's hook commands from every event and, when
// add is true, appends farero's entry. Entries of other tools are kept
// byte-for-byte.
func (c *Claude) rewriteHooks(settings *object, add bool) error {
	hooks, err := settings.child("hooks")
	if err != nil {
		return fmt.Errorf("settings.hooks: %w", err)
	}
	events := append([]string(nil), hooks.keys...)
	for _, ev := range hookEvents {
		if _, ok := hooks.get(ev); !ok {
			events = append(events, ev)
		}
	}
	for _, ev := range events {
		var entries []json.RawMessage
		if raw, ok := hooks.get(ev); ok {
			if err := json.Unmarshal(raw, &entries); err != nil {
				return fmt.Errorf("settings.hooks.%s: %w", ev, err)
			}
		}
		var kept []json.RawMessage
		for _, raw := range entries {
			cleaned, keep, err := stripFarero(raw)
			if err != nil {
				return fmt.Errorf("settings.hooks.%s: %w", ev, err)
			}
			if keep {
				kept = append(kept, cleaned)
			}
		}
		if add && contains(hookEvents, ev) {
			entry := map[string]any{"hooks": []map[string]any{{
				"type": "command", "command": c.hookCommand(), "timeout": HookTimeout,
			}}}
			if toolEvents[ev] {
				entry = map[string]any{"matcher": "*", "hooks": entry["hooks"]}
			}
			kept = append(kept, mustRaw(entry))
		}
		if len(kept) == 0 {
			hooks.del(ev)
		} else {
			hooks.set(ev, mustRaw(kept))
		}
	}
	if len(hooks.keys) == 0 {
		settings.del("hooks")
	} else {
		settings.set("hooks", mustRaw(hooks))
	}
	return nil
}

// stripFarero removes farero hook commands from one matcher entry. It
// returns the entry unchanged when it has none, and keep=false when nothing
// is left.
func stripFarero(raw json.RawMessage) (json.RawMessage, bool, error) {
	if !bytes.Contains(raw, []byte(hookMarker)) {
		return raw, true, nil
	}
	entry, err := parseObject(raw)
	if err != nil {
		return nil, false, err
	}
	hraw, ok := entry.get("hooks")
	if !ok {
		return raw, true, nil
	}
	var hooks []json.RawMessage
	if err := json.Unmarshal(hraw, &hooks); err != nil {
		return nil, false, err
	}
	var kept []json.RawMessage
	for _, h := range hooks {
		var probe struct {
			Command string `json:"command"`
		}
		json.Unmarshal(h, &probe)
		if strings.Contains(probe.Command, hookMarker) {
			continue
		}
		kept = append(kept, h)
	}
	if len(kept) == 0 {
		return nil, false, nil
	}
	entry.set("hooks", mustRaw(kept))
	return mustRaw(entry), true, nil
}

func setAllow(settings *object, add bool) error {
	perms, err := settings.child("permissions")
	if err != nil {
		return fmt.Errorf("settings.permissions: %w", err)
	}
	var allow []string
	if raw, ok := perms.get("allow"); ok {
		if err := json.Unmarshal(raw, &allow); err != nil {
			return fmt.Errorf("settings.permissions.allow: %w", err)
		}
	}
	var out []string
	for _, a := range allow {
		if a != AllowRule {
			out = append(out, a)
		}
	}
	if add {
		out = append(out, AllowRule)
	}
	if len(out) == 0 {
		perms.del("allow")
	} else {
		perms.set("allow", mustRaw(out))
	}
	if len(perms.keys) == 0 {
		settings.del("permissions")
	} else {
		settings.set("permissions", mustRaw(perms))
	}
	return nil
}

// render returns the current and the planned settings file contents.
func (c *Claude) render(install bool) (before, after string, err error) {
	b, err := os.ReadFile(c.settingsPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	settings, err := parseObject(b)
	if err != nil {
		return "", "", fmt.Errorf("%s is not valid JSON: %w", c.settingsPath(), err)
	}
	if err := c.rewriteHooks(settings, install); err != nil {
		return "", "", err
	}
	if err := setAllow(settings, install); err != nil {
		return "", "", err
	}
	out, err := pretty(settings)
	if err != nil {
		return "", "", err
	}
	return string(b), string(out), nil
}

// Plan describes what Apply would change.
func (c *Claude) Plan(ctx context.Context) (ipc.AgentCfgPlan, error) {
	before, after, err := c.render(true)
	if err != nil {
		return ipc.AgentCfgPlan{}, err
	}
	cli := c.findCLI(ctx)
	if cli == "" {
		cli = "claude"
	}
	return ipc.AgentCfgPlan{
		Agent:   "claude",
		Changes: []ipc.FileChange{{Path: c.settingsPath(), Before: before, After: after}},
		Commands: []string{
			fmt.Sprintf("%s mcp remove %s --scope user", cli, ServerName),
			fmt.Sprintf("%s mcp add-json %s '%s' --scope user", cli, ServerName, c.serverJSON()),
		},
	}, nil
}

// Apply installs farero after backing up settings.json.
func (c *Claude) Apply(ctx context.Context) (ipc.AgentCfgStatus, error) {
	cli := c.findCLI(ctx)
	if cli == "" {
		return ipc.AgentCfgStatus{}, errors.New("Claude Code(claude) 실행 파일을 찾을 수 없음")
	}
	backup, err := c.writeSettings(true)
	if err != nil {
		return ipc.AgentCfgStatus{}, err
	}
	written, _ := os.ReadFile(c.settingsPath())
	c.run(ctx, cli, "mcp", "remove", ServerName, "--scope", "user")
	if out, err := c.run(ctx, cli, "mcp", "add-json", ServerName, c.serverJSON(), "--scope", "user"); err != nil {
		return ipc.AgentCfgStatus{}, fmt.Errorf("claude mcp add-json: %v: %s", err, strings.TrimSpace(string(out)))
	}
	st := c.Status(ctx)
	st.BackupPath = backup
	// The claude CLI may rewrite settings.json itself while it runs (M0:
	// key order and "model" values changed). Say so, since the confirmed
	// diff did not show it.
	if now, err := os.ReadFile(c.settingsPath()); err == nil && !bytes.Equal(now, written) {
		st.Message = "Claude Code CLI가 등록 직후 settings.json을 스스로 다시 썼습니다(키 순서나 model 값 같은 Claude Code 자체 변경). farero 항목은 " +
			map[bool]string{true: "그대로 들어 있습니다.", false: "일부 빠졌습니다. 다시 등록해 주세요."}[st.HooksInstalled && st.AllowInstalled]
	}
	return st, nil
}

// Remove deletes only what farero added.
func (c *Claude) Remove(ctx context.Context) (ipc.AgentCfgStatus, error) {
	backup, err := c.writeSettings(false)
	if err != nil {
		return ipc.AgentCfgStatus{}, err
	}
	if cli := c.findCLI(ctx); cli != "" {
		c.run(ctx, cli, "mcp", "remove", ServerName, "--scope", "user")
	}
	st := c.Status(ctx)
	st.BackupPath = backup
	return st, nil
}

// FixPath rewrites farero's entries when the app moved (Q63): it backs up
// and updates the hook path and the gateway server without asking, and
// leaves a notice for the app. It does nothing unless farero is installed
// with a stale path.
func (c *Claude) FixPath(ctx context.Context) (bool, error) {
	st := c.Status(ctx)
	if !st.StalePath || !(st.HooksInstalled || st.MCPInstalled) {
		return false, nil
	}
	if st.HooksInstalled {
		if _, err := c.writeSettings(true); err != nil {
			return false, err
		}
	}
	if st.MCPInstalled && st.CLIFound {
		c.run(ctx, st.CLIPath, "mcp", "remove", ServerName, "--scope", "user")
		if out, err := c.run(ctx, st.CLIPath, "mcp", "add-json", ServerName, c.serverJSON(), "--scope", "user"); err != nil {
			return false, fmt.Errorf("claude mcp add-json: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	c.mu.Lock()
	c.notice = "farero 앱 위치가 바뀌어 Claude Code 설정의 경로를 새 위치로 고쳤습니다: " + c.HookPath
	c.mu.Unlock()
	return true, nil
}

// writeSettings backs up and rewrites settings.json. It returns the backup
// path ("" when there was no file).
func (c *Claude) writeSettings(install bool) (string, error) {
	before, after, err := c.render(install)
	if err != nil {
		return "", err
	}
	path := c.settingsPath()
	backup := ""
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
		if err := os.MkdirAll(c.BackupDir, 0o700); err != nil {
			return "", err
		}
		backup = filepath.Join(c.BackupDir, "claude-settings-"+time.Now().Format("20060102-150405.000")+".json")
		if err := os.WriteFile(backup, []byte(before), 0o600); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp := path + ".farero-tmp"
	if err := os.WriteFile(tmp, []byte(after), mode); err != nil {
		return "", err
	}
	return backup, os.Rename(tmp, path)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// parseVersion extracts "2.1.287" from "2.1.287 (Claude Code)".
func parseVersion(out string) string {
	for _, f := range strings.Fields(out) {
		if len(f) > 0 && f[0] >= '0' && f[0] <= '9' && strings.Count(f, ".") >= 1 {
			return f
		}
	}
	return ""
}

// versionAtLeast compares dotted numeric versions.
func versionAtLeast(v, min string) bool {
	if v == "" {
		return false
	}
	a, b := strings.Split(v, "."), strings.Split(min, ".")
	for i := 0; i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x, _ = strconv.Atoi(strings.TrimFunc(a[i], func(r rune) bool { return r < '0' || r > '9' }))
		}
		y, _ = strconv.Atoi(b[i])
		if x != y {
			return x > y
		}
	}
	return true
}
