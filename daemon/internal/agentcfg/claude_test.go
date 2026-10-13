package agentcfg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCLI emulates the parts of the claude CLI the installer uses.
type fakeCLI struct {
	version  string
	userJSON string
	calls    []string
}

func (f *fakeCLI) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if len(args) == 1 && args[0] == "--version" {
		return []byte(f.version + " (Claude Code)\n"), nil
	}
	doc := map[string]any{}
	if b, err := os.ReadFile(f.userJSON); err == nil {
		json.Unmarshal(b, &doc)
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	switch {
	case len(args) >= 3 && args[0] == "mcp" && args[1] == "add-json":
		var v any
		json.Unmarshal([]byte(args[3]), &v)
		servers[args[2]] = v
	case len(args) >= 3 && args[0] == "mcp" && args[1] == "remove":
		delete(servers, args[2])
	}
	doc["mcpServers"] = servers
	b, _ := json.Marshal(doc)
	return nil, os.WriteFile(f.userJSON, b, 0o600)
}

func setup(t *testing.T, settings string) (*Claude, *fakeCLI) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, ".claude")
	os.MkdirAll(cfg, 0o755)
	if settings != "" {
		os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(settings), 0o644)
	}
	f := &fakeCLI{version: "2.1.287", userJSON: filepath.Join(dir, ".claude.json")}
	c := &Claude{
		HookPath:   "/Applications/Farero.app/Contents/MacOS/farero-hook",
		GatewayURL: func() string { return "http://127.0.0.1:61511/mcp" },
		ConfigDir:  cfg, UserJSON: f.userJSON, BackupDir: filepath.Join(dir, "backups"),
		CLI: "/usr/local/bin/claude", Run: f.run,
	}
	return c, f
}

const userSettings = `{
  "model": "opus",
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "",
        "hooks": [
          { "type": "command", "command": "/usr/local/bin/other-tool && echo <done>", "timeout": 5, "async": true }
        ]
      }
    ],
    "PermissionRequest": [
      { "matcher": "", "hooks": [ { "type": "http", "url": "http://127.0.0.1:23333/permission", "timeout": 600 } ] }
    ]
  },
  "permissions": { "allow": ["Bash(npm test:*)"], "deny": [] },
  "statusLine": { "type": "command", "command": "echo hi" }
}
`

func TestInstallPreservesOtherSettings(t *testing.T) {
	c, f := setup(t, userSettings)
	ctx := context.Background()
	st, err := c.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.HooksInstalled || !st.AllowInstalled || !st.MCPInstalled || st.StalePath || !st.VersionOK {
		t.Fatalf("status after install: %+v", st)
	}
	if st.BackupPath == "" {
		t.Fatal("no backup")
	}
	if b, _ := os.ReadFile(st.BackupPath); string(b) != userSettings {
		t.Fatal("backup differs from the original")
	}
	b, _ := os.ReadFile(c.settingsPath())
	got := string(b)
	// Key order of the user's file is kept; farero's keys are appended.
	if strings.Index(got, `"model"`) > strings.Index(got, `"hooks"`) || strings.Index(got, `"permissions"`) > strings.Index(got, `"statusLine"`) {
		t.Fatalf("key order changed:\n%s", got)
	}
	for _, want := range []string{"other-tool && echo <done>", "127.0.0.1:23333", `"Bash(npm test:*)"`, AllowRule, `"timeout": 660`, `\"/Applications/Farero.app/Contents/MacOS/farero-hook\" --agent claude`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	var doc map[string]map[string][]map[string]any
	json.Unmarshal(b, &doc)
	if len(doc["hooks"]["SessionStart"]) != 1 || doc["hooks"]["SessionStart"][0]["matcher"] != nil {
		t.Errorf("SessionStart entry: %+v", doc["hooks"]["SessionStart"])
	}
	if len(doc["hooks"]["PreToolUse"]) != 1 || doc["hooks"]["PreToolUse"][0]["matcher"] != "*" {
		t.Errorf("PreToolUse entry: %+v", doc["hooks"]["PreToolUse"])
	}
	if len(doc["hooks"]["PermissionRequest"]) != 2 {
		t.Errorf("PermissionRequest should keep the other tool's entry: %+v", doc["hooks"]["PermissionRequest"])
	}
	srv, ok := c.installedServer()
	if !ok || srv.Timeout != ServerTimeoutMS || !strings.Contains(srv.HeadersHelper, "--headers") || srv.URL != "http://127.0.0.1:61511/mcp" {
		t.Fatalf("server: %+v %v (calls %v)", srv, ok, f.calls)
	}

	// Installing again changes nothing.
	if _, err := c.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(c.settingsPath())
	if string(b2) != got {
		t.Fatalf("second install changed the file:\n%s", b2)
	}

	// Remove: back to the user's file, byte for byte.
	if _, err := c.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	b3, _ := os.ReadFile(c.settingsPath())
	if string(b3) != userSettings {
		t.Fatalf("remove did not restore:\n%s\nwant:\n%s", b3, userSettings)
	}
	if _, ok := c.installedServer(); ok {
		t.Fatal("server still registered")
	}
}

func mustParse(t *testing.T, s string) *object {
	o, err := parseObject([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestInstallIntoMissingFile(t *testing.T) {
	c, _ := setup(t, "")
	st, err := c.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.BackupPath != "" || !st.HooksInstalled {
		t.Fatalf("%+v", st)
	}
	st, err = c.Remove(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(c.settingsPath()); !os.IsNotExist(err) {
		t.Fatalf("farero created settings.json, so remove should delete it: %s %v", b, err)
	}
	// The deleted file is backed up and the record of the original goes.
	if st.BackupPath == "" {
		t.Fatalf("no backup of the deleted file: %+v", st)
	}
	if _, err := os.Stat(c.originalPath()); !os.IsNotExist(err) {
		t.Fatalf("original record left behind: %v", err)
	}
}

// Changes the user (or Claude Code) made after installing are kept; only
// farero's entries go.
func TestRemoveKeepsLaterChanges(t *testing.T) {
	c, _ := setup(t, userSettings)
	ctx := context.Background()
	c.Apply(ctx)
	b, _ := os.ReadFile(c.settingsPath())
	o := mustParse(t, string(b))
	o.set("theme", json.RawMessage(`"dark"`))
	out, _ := pretty(o)
	os.WriteFile(c.settingsPath(), out, 0o644)

	if _, err := c.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(c.settingsPath())
	want := mustParse(t, userSettings)
	want.set("theme", json.RawMessage(`"dark"`))
	wb, _ := pretty(want)
	if string(b) != string(wb) {
		t.Fatalf("after remove:\n%s\nwant:\n%s", b, wb)
	}
}

// The claude CLI rewrites settings.json while farero registers the MCP
// server (key order). That alone does not stop an exact restore.
func TestRemoveRestoresAfterCLIReorder(t *testing.T) {
	c, _ := setup(t, userSettings)
	ctx := context.Background()
	c.Apply(ctx)
	b, _ := os.ReadFile(c.settingsPath())
	var v map[string]any
	json.Unmarshal(b, &v)
	nb, _ := json.Marshal(v) // sorted keys, compact
	os.WriteFile(c.settingsPath(), nb, 0o644)

	c.Remove(ctx)
	if b, _ := os.ReadFile(c.settingsPath()); string(b) != userSettings {
		t.Fatalf("not restored:\n%s", b)
	}
}

// Reinstalling (or fixing the path) keeps the original from the first
// install.
func TestReinstallKeepsFirstOriginal(t *testing.T) {
	c, _ := setup(t, userSettings)
	ctx := context.Background()
	c.Apply(ctx)
	c.Apply(ctx)
	c.HookPath = "/Users/me/Applications/Farero.app/Contents/MacOS/farero-hook"
	c.FixPath(ctx)
	c.Remove(ctx)
	if b, _ := os.ReadFile(c.settingsPath()); string(b) != userSettings {
		t.Fatalf("not restored:\n%s", b)
	}
}

func TestPlanShowsDiffWithoutWriting(t *testing.T) {
	c, _ := setup(t, userSettings)
	p, err := c.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 1 || p.Changes[0].Before != userSettings || !strings.Contains(p.Changes[0].After, AllowRule) {
		t.Fatalf("plan: %+v", p)
	}
	if len(p.Commands) != 2 || !strings.Contains(p.Commands[1], "add-json farero") {
		t.Fatalf("commands: %v", p.Commands)
	}
	b, _ := os.ReadFile(c.settingsPath())
	if string(b) != userSettings {
		t.Fatal("plan wrote the file")
	}
}

func TestFixPathAfterMove(t *testing.T) {
	c, _ := setup(t, userSettings)
	ctx := context.Background()
	c.Apply(ctx)
	if fixed, _ := c.FixPath(ctx); fixed {
		t.Fatal("nothing to fix yet")
	}
	c.HookPath = "/Users/me/Applications/Farero.app/Contents/MacOS/farero-hook"
	st := c.Status(ctx)
	if !st.StalePath {
		t.Fatalf("stale path not detected: %+v", st)
	}
	fixed, err := c.FixPath(ctx)
	if err != nil || !fixed {
		t.Fatalf("fix: %v %v", fixed, err)
	}
	st = c.Status(ctx)
	if st.StalePath || !st.HooksInstalled || !strings.Contains(st.Message, "고쳤습니다") {
		t.Fatalf("after fix: %+v", st)
	}
	// The notice says where the previous file went (Q63: back up, then fix).
	if !strings.Contains(st.Message, c.BackupDir) {
		t.Fatalf("notice without the backup path: %q", st.Message)
	}
	b, _ := os.ReadFile(c.settingsPath())
	if strings.Count(string(b), "farero-hook") != len(hookEvents) {
		t.Fatalf("expected one farero entry per event:\n%s", b)
	}
	if srv, _ := c.installedServer(); !strings.Contains(srv.HeadersHelper, "/Users/me/Applications") {
		t.Fatalf("server not updated: %+v", srv)
	}
}

func TestOldClaudeVersion(t *testing.T) {
	c, f := setup(t, "")
	f.version = "2.1.150"
	if st := c.Status(context.Background()); st.VersionOK || st.Version != "2.1.150" {
		t.Fatalf("%+v", st)
	}
}

func TestVersionCompare(t *testing.T) {
	for _, c := range []struct {
		v    string
		want bool
	}{
		{"2.1.203", true}, {"2.1.287", true}, {"2.2.0", true}, {"3.0.0", true}, {"2.1.202", false}, {"2.0.999", false}, {"", false},
		// Fewer parts count the missing ones as 0; more parts are fine.
		{"2.1", false}, {"2", false}, {"3", true}, {"2.1.203.1", true},
		// Digits at the ends of a part are kept, 0 and 9 included.
		{"2.1.210", true}, {"2.1.209", true}, {"2.1.290", true}, {"2.1.200", false},
		// Non-digits around a part are dropped.
		{"2.1.205-beta", true}, {"2.1.150-beta", false},
	} {
		if got := versionAtLeast(c.v, MinClaudeVersion); got != c.want {
			t.Errorf("%q: %v", c.v, got)
		}
	}
}

// The version is the first word that starts with a digit and has a dot.
func TestParseVersion(t *testing.T) {
	for out, want := range map[string]string{
		"2.1.287 (Claude Code)": "2.1.287",
		"Claude Code 2.1.287":   "2.1.287",
		"0.9.1":                 "0.9.1",
		"9.0":                   "9.0",
		"build 7 of 2.1.3":      "2.1.3", // 7 has no dot
		"a.b 2.1":               "2.1",
		"/1.2 3.4":              "3.4", // '/' sorts just below '0'
		":1.2 3.4":              "3.4", // ':' sorts just above '9'
		"Claude Code":           "",
		"":                      "",
		"  2.1.287\n":           "2.1.287",
	} {
		if got := parseVersion(out); got != want {
			t.Errorf("parseVersion(%q) = %q, want %q", out, got, want)
		}
	}
}

func TestInvalidSettingsAreNotTouched(t *testing.T) {
	// Broken JSON, and valid JSON that is not an object.
	for _, bad := range []string{`{"hooks": [`, `[]`, `"x"`, `[{"hooks": {}}]`} {
		c, _ := setup(t, bad)
		if _, err := c.Apply(context.Background()); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
		if b, _ := os.ReadFile(c.settingsPath()); string(b) != bad {
			t.Errorf("%s: file was modified to %s", bad, b)
		}
	}
}

func TestApplyReportsCLIRewrite(t *testing.T) {
	c, f := setup(t, userSettings)
	inner := f.run
	c.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, err := inner(ctx, name, args...)
		if len(args) > 1 && args[1] == "add-json" {
			// Emulate the CLI rewriting settings.json (reordering keys).
			b, _ := os.ReadFile(c.settingsPath())
			var v map[string]any
			json.Unmarshal(b, &v)
			nb, _ := json.Marshal(v)
			os.WriteFile(c.settingsPath(), nb, 0o644)
		}
		return out, err
	}
	st, err := c.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.Message, "다시 썼습니다") || !strings.Contains(st.Message, "그대로") {
		t.Fatalf("message: %q", st.Message)
	}
}

// editServer changes one field of farero's MCP entry in ~/.claude.json, as
// an older farero (other port or hook path) or the user would have left it.
func editServer(t *testing.T, path, field string, v any) {
	t.Helper()
	var doc map[string]any
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	doc["mcpServers"].(map[string]any)[ServerName].(map[string]any)[field] = v
	b, _ = json.Marshal(doc)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Any one field of the MCP entry that differs from what farero would
// register (the gateway port, the headersHelper path, the timeout) makes
// the registration stale; the hooks alone being right is not enough.
func TestStatusFlagsEachStaleServerField(t *testing.T) {
	c, _ := setup(t, userSettings)
	ctx := context.Background()
	for field, v := range map[string]any{
		"url":           "http://127.0.0.1:1/mcp",
		"headersHelper": "/old/farero-hook --headers --agent claude",
		"timeout":       60000,
	} {
		if _, err := c.Apply(ctx); err != nil {
			t.Fatal(err)
		}
		if st := c.Status(ctx); !st.MCPInstalled || !st.HooksInstalled || st.StalePath {
			t.Fatalf("fresh install: %+v", st)
		}
		editServer(t, c.UserJSON, field, v)
		if st := c.Status(ctx); !st.MCPInstalled || !st.HooksInstalled || !st.StalePath {
			t.Errorf("%s changed: %+v", field, st)
		}
	}
}

// Status reports the CLI it was given, not one found on this machine.
func TestStatusUsesConfiguredCLI(t *testing.T) {
	c, _ := setup(t, "")
	if st := c.Status(context.Background()); !st.CLIFound || st.CLIPath != "/usr/local/bin/claude" || st.Version != "2.1.287" {
		t.Fatalf("%+v", st)
	}
}

// Apply says the CLI rewrote settings.json only when it did, and then says
// "some are missing" unless both the hooks and the allow rule survived.
func TestApplyRewriteMessage(t *testing.T) {
	c, _ := setup(t, userSettings)
	st, err := c.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(st.Message, "다시 썼습니다") {
		t.Fatalf("no rewrite, but: %q", st.Message)
	}
	for name, edit := range map[string]func(map[string]any){
		"allow dropped": func(v map[string]any) {
			v["permissions"].(map[string]any)["allow"] = []any{"Bash(npm test:*)"}
		},
		"hooks dropped": func(v map[string]any) { delete(v, "hooks") },
	} {
		c, f := setup(t, userSettings)
		inner := f.run
		c.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			out, err := inner(ctx, name, args...)
			if len(args) > 1 && args[1] == "add-json" {
				b, _ := os.ReadFile(c.settingsPath())
				var v map[string]any
				json.Unmarshal(b, &v)
				edit(v)
				nb, _ := json.Marshal(v)
				os.WriteFile(c.settingsPath(), nb, 0o644)
			}
			return out, err
		}
		st, err := c.Apply(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(st.Message, "다시 썼습니다") || !strings.Contains(st.Message, "일부 빠졌습니다") {
			t.Errorf("%s: %q", name, st.Message)
		}
	}
}

// Every hook event is checked: one event missing farero's hook does not
// hide a stale path in the others.
func TestHooksStateChecksEveryEvent(t *testing.T) {
	c, _ := setup(t, userSettings)
	ctx := context.Background()
	if _, err := c.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	o := mustParse(t, readFile(t, c.settingsPath()))
	hooks, _ := o.child("hooks")
	hooks.del(hookEvents[0])
	o.set("hooks", mustRaw(hooks))
	out, _ := pretty(o)
	os.WriteFile(c.settingsPath(), out, 0o644)
	c.HookPath = "/Users/me/Applications/Farero.app/Contents/MacOS/farero-hook"
	// hooksState itself: Status would also flag the MCP entry, whose
	// headersHelper names the old path too.
	installed, present, stale := c.hooksState(mustParse(t, readFile(t, c.settingsPath())))
	if installed || !present || !stale {
		t.Fatalf("installed=%v present=%v stale=%v", installed, present, stale)
	}
}

// Without the record of the original, Remove cannot know farero created
// settings.json: it leaves an empty object instead of deleting the file.
func TestRemoveWithoutOriginalKeepsFile(t *testing.T) {
	c, _ := setup(t, "")
	ctx := context.Background()
	if _, err := c.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(c.originalPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(readFile(t, c.settingsPath()), "{}") {
		t.Fatalf("settings: %q", readFile(t, c.settingsPath()))
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A user hook that shares a matcher entry with farero's, before or after
// it, stays when farero is removed.
func TestRemoveKeepsUserHookInFareroEntry(t *testing.T) {
	c, _ := setup(t, "")
	ctx := context.Background()
	if _, err := c.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal([]byte(readFile(t, c.settingsPath())), &doc)
	entries := doc["hooks"].(map[string]any)["PreToolUse"].([]any)
	entry := entries[0].(map[string]any)
	user := map[string]any{"type": "command", "command": "/usr/local/bin/audit-tool"}
	entry["hooks"] = append(entry["hooks"].([]any), user)
	entries = append(entries, map[string]any{"matcher": "Bash", "hooks": []any{user, entry["hooks"].([]any)[0]}})
	doc["hooks"].(map[string]any)["PreToolUse"] = entries
	b, _ := json.Marshal(doc)
	os.WriteFile(c.settingsPath(), b, 0o644)

	if _, err := c.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, c.settingsPath())
	if strings.Contains(after, "farero-hook") {
		t.Fatalf("farero left:\n%s", after)
	}
	if n := strings.Count(after, "/usr/local/bin/audit-tool"); n != 2 {
		t.Fatalf("user hooks kept: %d\n%s", n, after)
	}
}

// Apply keeps settings.json's file mode.
func TestApplyKeepsFileMode(t *testing.T) {
	c, _ := setup(t, userSettings)
	if err := os.Chmod(c.settingsPath(), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(c.settingsPath()); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

// Farero adds its hook only to its own events; the user's other events are
// left as they are.
func TestApplyLeavesOtherEvents(t *testing.T) {
	const settings = `{"hooks":{"PreCompact":[{"hooks":[{"type":"command","command":"/usr/local/bin/save-notes"}]}]}}`
	c, _ := setup(t, settings)
	if _, err := c.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, c.settingsPath())
	if n := strings.Count(after, "farero-hook"); n != len(hookEvents) {
		t.Fatalf("%d farero entries, want %d:\n%s", n, len(hookEvents), after)
	}
	o := mustParse(t, after)
	hooks, _ := o.child("hooks")
	if raw, _ := hooks.get("PreCompact"); strings.Contains(string(raw), "farero-hook") || !strings.Contains(string(raw), "save-notes") {
		t.Fatalf("PreCompact: %s", raw)
	}
}

// StopFailure ends a turn on an API error instead of Stop (v2.1.78); without
// it the session would stay "running".
func TestInstallRegistersStopFailure(t *testing.T) {
	c, _ := setup(t, "")
	c.Apply(context.Background())
	var doc struct {
		Hooks map[string][]hookEntry `json:"hooks"`
	}
	b, _ := os.ReadFile(c.settingsPath())
	json.Unmarshal(b, &doc)
	if e := doc.Hooks["StopFailure"]; len(e) != 1 || !strings.Contains(e[0].Hooks[0].Command, "farero-hook") {
		t.Fatalf("StopFailure: %+v", doc.Hooks["StopFailure"])
	}
}

// Claude Code before v2.1.101 ignores the whole settings.json when it has a
// hook event it does not know (M2 실험, CHANGELOG). farero does not write
// into a Claude Code older than it supports (Q53).
func TestOldClaudeVersionIsNotRegistered(t *testing.T) {
	c, f := setup(t, userSettings)
	f.version = "2.1.150"
	ctx := context.Background()
	if _, err := c.Plan(ctx); err == nil || !strings.Contains(err.Error(), MinClaudeVersion) {
		t.Fatalf("plan: %v", err)
	}
	if _, err := c.Apply(ctx); err == nil || !strings.Contains(err.Error(), MinClaudeVersion) {
		t.Fatalf("apply: %v", err)
	}
	if b, _ := os.ReadFile(c.settingsPath()); string(b) != userSettings {
		t.Fatal("settings.json was changed")
	}
	if len(f.calls) != 2 || f.calls[0] != "--version" {
		t.Fatalf("claude was run for more than its version: %v", f.calls)
	}
}

// The automatic fix after the app moved changes paths only (Q63): it does
// not add events a newer farero listens to, which needs the user's
// confirmation of the diff.
func TestFixPathKeepsEvents(t *testing.T) {
	c, _ := setup(t, "")
	ctx := context.Background()
	c.Apply(ctx)
	// An install from before StopFailure was registered.
	b, _ := os.ReadFile(c.settingsPath())
	o := mustParse(t, string(b))
	hooks, _ := o.child("hooks")
	hooks.del("StopFailure")
	o.set("hooks", mustRaw(hooks))
	out, _ := pretty(o)
	os.WriteFile(c.settingsPath(), out, 0o644)

	c.HookPath = "/Users/me/Applications/Farero.app/Contents/MacOS/farero-hook"
	if fixed, err := c.FixPath(ctx); err != nil || !fixed {
		t.Fatalf("fix: %v %v", fixed, err)
	}
	b, _ = os.ReadFile(c.settingsPath())
	if strings.Contains(string(b), "StopFailure") {
		t.Fatalf("fix added an event:\n%s", b)
	}
	if strings.Count(string(b), "/Users/me/Applications/") != len(hookEvents)-1 {
		t.Fatalf("paths not all fixed:\n%s", b)
	}
}

// Only the gateway entry is stale (the port changed): settings.json is not
// rewritten or backed up, and a second run finds nothing to do.
func TestFixPathOnlyWhatIsStale(t *testing.T) {
	c, _ := setup(t, userSettings)
	ctx := context.Background()
	c.Apply(ctx)
	before, _ := os.ReadFile(c.settingsPath())
	backups, _ := os.ReadDir(c.BackupDir)
	c.GatewayURL = func() string { return "http://127.0.0.1:61999/mcp" }

	fixed, err := c.FixPath(ctx)
	if err != nil || !fixed {
		t.Fatalf("fix: %v %v", fixed, err)
	}
	if after, _ := os.ReadFile(c.settingsPath()); string(after) != string(before) {
		t.Fatal("settings.json rewritten")
	}
	if now, _ := os.ReadDir(c.BackupDir); len(now) != len(backups) {
		t.Fatalf("new backup: %d -> %d", len(backups), len(now))
	}
	if srv, _ := c.installedServer(); srv.URL != "http://127.0.0.1:61999/mcp" {
		t.Fatalf("server: %+v", srv)
	}
	if fixed, _ := c.FixPath(ctx); fixed {
		t.Fatal("fixed again")
	}
}

// Writes never leave a half-written settings.json, even when the startup
// path fix and a request from the app overlap.
func TestConcurrentWritesKeepValidJSON(t *testing.T) {
	c, _ := setup(t, userSettings)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 20; j++ {
				mode := modeInstall
				if (i+j)%2 == 1 {
					mode = modeRemove
				}
				c.writeSettings(mode)
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	b, _ := os.ReadFile(c.settingsPath())
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, b)
	}
}

// An event whose hook list cannot be read counts as not installed, and the
// events after it are still checked.
func TestHooksStateSkipsUnreadableEvent(t *testing.T) {
	c, _ := setup(t, userSettings)
	if _, err := c.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	o := mustParse(t, readFile(t, c.settingsPath()))
	hooks, _ := o.child("hooks")
	hooks.set(hookEvents[0], json.RawMessage(`"not a list"`))
	o.set("hooks", mustRaw(hooks))
	c.HookPath = "/Users/me/Applications/Farero.app/Contents/MacOS/farero-hook"
	installed, present, stale := c.hooksState(o)
	if installed || !present || !stale {
		t.Fatalf("installed=%v present=%v stale=%v", installed, present, stale)
	}
}
