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
- `Farero.app`: the UI. It opens at login too, unless you turn that off in **Settings › 일반 › 로그인할 때 Farero 열기**.

If the app isn't running, Claude Code is never blocked:
- Its own permission prompts fall back to the terminal.
- Gateway calls that need approval are refused, with a reason.

## Install

Requirements: macOS 15 or later (Apple silicon or Intel) and Claude Code v2.1.203 or later.

1. Download `Farero-<version>.zip` from [Releases](https://github.com/farero-dev/farero-dev.github.io/releases) and unzip it. To check the download, compare `shasum -a 256 Farero-<version>.zip` with the `.sha256` file next to it.
2. **Move `Farero.app` to `/Applications` before you open it.** If you open it from Downloads, macOS runs a translocated copy from a random read-only path, and farero refuses to register its daemon from there and asks you to move it.
3. The app is not notarized yet, so macOS blocks the first launch. Open it once, then go to **System Settings › Privacy & Security** and click **Open Anyway**. Since macOS 15, Control-click › Open no longer bypasses this.
4. On first launch farero installs its daemon, `farerod`, as a LaunchAgent (`~/Library/LaunchAgents/dev.farero.farerod.plist`), and macOS shows a "Background Items Added" notification. Keep it on in **System Settings › General › Login Items & Extensions**. If it is off, farero tells you and opens that page.
   The app also writes a second LaunchAgent, `~/Library/LaunchAgents/dev.farero.app.plist`, that opens Farero when you log in. Approval cards need the app, and while it is not running, gateway calls that need approval are refused. Turn it off in **Settings › 일반 › 로그인할 때 Farero 열기**. If you quit Farero from its menu, it stays closed until your next login.
5. If macOS asks whether `farerod` may use its Keychain items (the gateway secret and plugin tokens), enter your login password and choose **Always Allow**. It asks again after every update. Until you answer, the app and hooks still work, but the gateway waits.
6. In the app, register farero with Claude Code (**Settings › Claude Code**, or the setup assistant). farero shows a diff of the changes and asks for confirmation before writing anything.

### What farero changes in Claude Code

| Where | What |
|---|---|
| `~/.claude/settings.json` → `hooks` | `farero-hook` for SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, PostToolUseFailure, PermissionRequest, Notification, Stop, StopFailure and SessionEnd. Timeout 660 s, so a 10-minute approval can finish. |
| `~/.claude/settings.json` → `permissions.allow` | `mcp__farero__*`, so gateway tools are approved in farero instead of in Claude Code. |
| user-scope MCP server `farero` | Added with `claude mcp add-json`: `http://127.0.0.1:<port>/mcp`, a `headersHelper`, and a 660 s timeout. |

farero backs up the original file first. **Remove settings** deletes only what farero added; if nothing else changed in the meantime, the file is restored byte for byte. farero does not register with a Claude Code older than v2.1.203.

### Permissions farero asks for

- **GitHub:** OAuth device flow with scopes `repo` and `read:org`.
- **Railway:** OAuth in your browser (PKCE, loopback redirect) with scopes `openid profile email offline_access workspace:member`. Project tokens are not accepted.
- **Resend:** OAuth with scope `full_access`. Only email-related tools are exposed. API keys, domains and webhooks are not.
- **Gmail:** `gmail.readonly` only, through an OAuth client you create in your own Google Cloud project. The app walks you through it.
- **Automation (Terminal.app):** used only to jump to the terminal tab of a session.

### Your data stays on your Mac

- The log and settings are in `~/Library/Application Support/Farero`. Tokens are in the macOS Keychain.
- There are no accounts, no server and no telemetry.
- The only request the app makes on its own is checking GitHub Releases for a new version. You can turn it off in Settings.
- Tokens and keys never appear in logs or on screen.

## Updating

1. Quit Farero from its menu.
2. Replace `/Applications/Farero.app` with the new one and open it.
3. The app restarts `farerod` on its own when the daemon in the new bundle differs from the running one. Pending approvals finish first.
4. macOS asks for Keychain access again after every update (ad-hoc signing changes the code signature). Enter your password and choose **Always Allow**.

The app checks GitHub Releases once at launch and then once a day, and shows a notice when a newer version exists. Turn it off in **Settings › 일반 › 업데이트 확인**. This is the only network request the app makes on its own.

## Uninstall

1. In the app, open **Settings › Claude Code** and click **설정 제거…**, then confirm with **설정 제거**. This removes only what farero added to Claude Code (hooks, `mcp__farero__*` rule, the `farero` MCP server).
2. Quit Farero from its menu.
3. Remove the daemon and the app:

```sh
launchctl bootout gui/$(id -u)/dev.farero.farerod
launchctl bootout gui/$(id -u)/dev.farero.app
rm ~/Library/LaunchAgents/dev.farero.farerod.plist ~/Library/LaunchAgents/dev.farero.app.plist
rm -rf /Applications/Farero.app
```

4. Delete the data (database, config, socket, backups of your Claude Code files) and the daemon log: `rm -rf ~/Library/Application\ Support/Farero ~/Library/Logs/Farero`. This deletes the audit log.
5. Delete the Keychain items. All of them have the service `dev.farero.farerod`. Their accounts are `gateway.secret` and `plugin.<plugin>.<what>` (for example `plugin.github.token`, `plugin.railway.token`, `plugin.railway.setup`, `plugin.resend.token`, `plugin.resend.setup`, `plugin.gmail.token`, `plugin.gmail.client`). Run once per item, or find them in Keychain Access by searching for `dev.farero.farerod`:

```sh
security delete-generic-password -s dev.farero.farerod -a gateway.secret
security delete-generic-password -s dev.farero.farerod -a plugin.github.token
# ...and so on for each plugin you connected
```

6. Delete the app's preferences: `defaults delete dev.farero.Farero` (and `rm -f ~/Library/Preferences/dev.farero.Farero.plist` if it remains).

## Troubleshooting

- **The daemon is not running.** Open **System Settings › General › Login Items & Extensions** and allow Farero to run in the background. The menu item **데몬 상태…** shows what the app sees. If registration failed, the menu offers **로그인 항목에서 데몬 허용…**.
- **Farero does not open at login.** Check **Settings › 일반 › 로그인할 때 Farero 열기**. If it says the item is off in Login Items, allow Farero to run in the background in **System Settings › General › Login Items & Extensions**.
- **The gateway does not start.** farerod reads the Keychain after it opens its socket. Until you answer the Keychain prompt, the gateway waits. If you chose **Deny**, the menu shows a gateway error and plugin tools are missing; restart the daemon to be asked again: `launchctl kickstart -k gui/$(id -u)/dev.farero.farerod`.
- **Claude Code is too old.** farero does not register with Claude Code older than v2.1.203. Update Claude Code, then register again.
- **You moved the app.** Hook and MCP paths point into the app bundle. On the next launch farero fixes the paths it added (after a backup) and tells you. If **Settings › Claude Code** still shows a stale path, click **다시 등록…**.
- **A card was answered by something else.** Other apps can register their own `PermissionRequest` hook in your Claude Code settings, and their answer can override farero's. Check `~/.claude/settings.json` for other hooks. Answering at the terminal first also wins over farero; the log shows "취소됨 · 터미널에서 먼저 답함".

## Development

Requirements:
- macOS 15 or later
- Xcode with Swift 6
- Go 1.27 (Homebrew; `modernc.org/sqlite` needs Go 1.26 or later)

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

- `scripts/e2e.sh` (gateway, approvals) and `scripts/e2e-sessions.py` (session states with two interactive sessions) run a real Claude Code against a development farerod, isolated from your own config.
- `docs/ipc.md` describes the socket protocol.
- `policy/default.json` is the default tool classification.
- `log.md` is the development log.

## License

[MIT](LICENSE)
