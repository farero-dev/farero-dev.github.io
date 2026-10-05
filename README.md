# farero

**farero** (Spanish for "lighthouse keeper") is an open-source macOS app that lives in the notch and the menu bar. It watches your AI coding agent sessions and lets you approve what they do.

- **Agent permissions in the notch.** When Claude Code asks to run a shell command or edit a file, the request shows up as a card in the notch: allow, allow for this session, or deny. The card never takes keyboard focus, and it has global shortcuts.
- **One gateway for external services.** Agents reach Railway, GitHub, Gmail (read-only) and Resend only through farero's local MCP gateway. Each tool is auto-allowed, asks you, or is blocked, according to a classification table that ships with the app and that you can adjust.
- **Taint rule.** After a session reads untrusted content (an email, an issue, a received message, a file from a repo), its "allow for this session" grants stop counting. From then on, every write asks again, including the agent's own shell commands.
- **Audit log.** Every gateway call and every agent permission decision is recorded per session, with full-text search (Korean too). The log is kept until you delete the session.

> Status: under active development toward the MVP. Supported agent: Claude Code (v2.1.203 or later). Codex CLI support is planned after the MVP.

## How it works

```
Claude Code ──hooks──▶ farero-hook ──unix socket──▶ farerod ◀──unix socket── Farero.app (notch, menu bar)
     │                                                 │
     └──── MCP (http://127.0.0.1) ───────────────────▶ gateway ──▶ GitHub · Railway · Resend MCP, Gmail API
```

- `farerod`: the daemon. It runs as a login item and holds sessions, the approval queue, the policy, the gateway, the log (SQLite) and the tokens (Keychain).
- `farero-hook`: a small helper that Claude Code runs for each hook event.
- `Farero.app`: the UI.

If the app isn't running, Claude Code is never blocked:
- Its own permission prompts fall back to the terminal.
- Gateway calls that need approval are refused, with a reason.

## Install

1. Download `Farero-<version>.zip` from [Releases](https://github.com/farero-dev/farero-dev.github.io/releases), unzip it, and move `Farero.app` to `/Applications`.
2. The app is not notarized yet, so macOS blocks the first launch. Open it once, then go to **System Settings › Privacy & Security** and click **Open Anyway**. Since macOS Sequoia, Control-click › Open no longer bypasses this.
3. Allow farero's background item when macOS asks (**System Settings › General › Login Items**). This is the `farerod` daemon. Because the app is ad-hoc signed, macOS may ask again after an update or a reboot. farero detects this and shows where to turn it back on.
4. In the app, register farero with Claude Code. farero shows a diff of the changes and asks for confirmation before writing anything.

### What farero changes in Claude Code

| Where | What |
|---|---|
| `~/.claude/settings.json` → `hooks` | `farero-hook` for SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, PostToolUseFailure, PermissionRequest, Notification, Stop and SessionEnd. Timeout 660 s, so a 10-minute approval can finish. |
| `~/.claude/settings.json` → `permissions.allow` | `mcp__farero__*`, so gateway tools are approved in farero instead of in Claude Code. |
| user-scope MCP server `farero` | Added with `claude mcp add-json`: `http://127.0.0.1:<port>/mcp`, a `headersHelper`, and a 660 s timeout. |

farero backs up the original file first. **Remove settings** deletes only what farero added.

### Permissions farero asks for

- **GitHub:** OAuth device flow with scopes `repo` and `read:org`.
- **Railway:** OAuth device flow with scopes `openid profile email offline_access workspace:member`. Project tokens are not accepted.
- **Resend:** OAuth with scope `full_access`. Only email-related tools are exposed. API keys, domains and webhooks are not.
- **Gmail:** `gmail.readonly` only, through an OAuth client you create in your own Google Cloud project. The app walks you through it.
- **Automation (Terminal.app):** used only to jump to the terminal tab of a session.

### Your data stays on your Mac

- The log and settings are in `~/Library/Application Support/Farero`. Tokens are in the macOS Keychain.
- There are no accounts, no server and no telemetry.
- The only request the app makes on its own is checking GitHub Releases for a new version. You can turn it off in Settings.
- Tokens and keys never appear in logs or on screen.

## Development

Requirements:
- macOS 15 or later
- Xcode with Swift 6
- Go 1.26 or later

```sh
# daemon (Go)
cd daemon && go test ./...

# character and app (Swift)
swift test --package-path app/LighthouseKit
swift test --package-path app

# Universal Farero.app + zip in build/
scripts/build.sh 0.1.0
```

Development runs never touch your real install or Claude Code config:

```sh
cd daemon && go build -tags farero_dev -o ../build/dev/ ./cmd/...   # includes a fake "dev" plugin
mkdir -p /tmp/fr
FARERO_HOME=/tmp/fr FARERO_SOCKET=/tmp/fr/d.sock FARERO_CLAUDE_CONFIG_DIR=/tmp/fr/claude ../build/dev/farerod --dev
FARERO_SOCKET=/tmp/fr/d.sock swift run --package-path app Farero
```

- `docs/ipc.md` describes the socket protocol.
- `policy/default.json` is the default tool classification.
- `log.md` is the development log.

## License

[MIT](LICENSE)
