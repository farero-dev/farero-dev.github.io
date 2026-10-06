# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

farero is an open-source macOS notch/menu-bar app (MIT). It watches AI coding agent sessions (MVP: Claude Code only; Codex comes after the MVP) and acts as a local MCP gateway: plugin tools (Railway, GitHub, Gmail read-only, Resend) reach agents only through `farerod`, which applies an approval policy, asks in the notch when needed, and writes a searchable audit log.

The design lives in Notion. Read it before changing behavior. When documents disagree, 기능 명세서 wins over 아키텍처 상세 설계, which wins over MVP 구현 순서.

- Project page: [https://app.notion.com/p/3ecc8fd1afc9811cb6e1c3bd15753ade](https://app.notion.com/p/3ecc8fd1afc9811cb6e1c3bd15753ade)
- 기능 명세서 (rules: what and how): [https://app.notion.com/p/3ecc8fd1afc981eb8472c345e49aba8d](https://app.notion.com/p/3ecc8fd1afc981eb8472c345e49aba8d)
- 아키텍처 상세 설계 (processes, IPC, storage): [https://app.notion.com/p/3ecc8fd1afc9819aa130d931cc7ebb4d](https://app.notion.com/p/3ecc8fd1afc9819aa130d931cc7ebb4d)
- 구현 전 검증 결과 (verified facts, M0 results go here): [https://app.notion.com/p/3ecc8fd1afc9814dae39c9681d998f18](https://app.notion.com/p/3ecc8fd1afc9814dae39c9681d998f18)
- MVP 구현 순서 (milestones M0–M8): [https://app.notion.com/p/3ecc8fd1afc981718c27e939f68ebc4f](https://app.notion.com/p/3ecc8fd1afc981718c27e939f68ebc4f)

Decisions are tagged `Q<number>` in those docs, and code comments cite them (for example `Q46`). Design docs are in Korean; code and comments are in English.

## Commands

Go 1.27 (Homebrew). `modernc.org/sqlite` needs Go 1.26 or newer.

```sh
cd daemon
go test ./...                                         # all daemon tests
go test ./internal/policy -run TestDecidePluginOrder  # one test
go generate ./internal/policy                         # re-copy policy/default.json into the embed dir
go vet ./...
```

End-to-end checks with a real, interactive Claude Code (headless pseudo-terminals, an isolated Claude config dir, a dev farerod under `/tmp`; they take several minutes): `scripts/e2e-sessions.py` (M2 session watching), `scripts/e2e-approvals.py` (M3 agent tool approval; `E2E_APPROVAL_TIMEOUT=10m` for the real deadline), `scripts/e2e.sh` (`claude -p` through the gateway). The shared harness is `scripts/e2elib.py`.

`experiments/` is a separate Go module (`cd experiments && go build -o bin/<name> ./<dir>`). It holds M0 throwaway code and must never be imported by `daemon/` (Q67).

## Architecture

There are three processes. All state lives in the daemon (Q40):

- **`farerod`** (Go, `daemon/cmd/farerod`): a user LaunchAgent (`~/Library/LaunchAgents/dev.farero.farerod.plist`) that the app writes and loads with `launchctl`. Not `SMAppService`: with ad-hoc signing it ties the job to farerod's code hash, and launchd refuses the farerod of every updated bundle. It owns sessions, the approval broker, the policy engine, the MCP gateway (Streamable HTTP on `127.0.0.1:<port fixed on first run>`), upstream plugin connections, SQLite and Keychain.
- **`farero-hook`** (Go, `daemon/cmd/farero-hook`): Claude Code runs it for every hook event (`farero-hook --agent claude`) and as the MCP `headersHelper` (`--headers`). It forwards to `farerod` over the Unix socket. If the socket is unreachable it exits 0 with no output, so the agent is never blocked.
- **App** (Swift, `app/`, planned as a SwiftPM package): a UI client of `farerod` over the same socket. It handles the notch and menu bar, approval cards, settings, log search and terminal jump.

IPC uses JSON Lines over `~/Library/Application Support/Farero/farerod.sock` (mode 0600). Every message is `{"type","id","data"}`. The JSON field names in `daemon/internal/model` are the IPC contract the Swift app decodes, so renaming one is a protocol change.

Packages under `daemon/internal/`: `core` (wires everything; the socket server), `ipc`, `session`, `broker`, `policy`, `correlate`, `gateway`, `upstream`, `plugins`, `auth`, `store`, `secret`, `agentcfg`, `model`, `paths`.

farerod opens its socket before anything reads the Keychain (gateway secret, plugin tokens). With ad-hoc signing every update asks for Keychain access again; until the user answers, the app and the hooks still reach farerod, and it re-sends `state.snapshot` once the gateway is up.

### Policy (기능 명세서 6장, `internal/policy`)

- The classification table's source of truth is `policy/default.json`. `daemon/internal/policy/default.json` is an embedded copy, and a test fails when the two differ. A tool missing from the table is never exposed to agents.
- Each tool has a level (`auto` / `ask` / `block`) plus flags: `no_session` (approve every time; the user cannot lift it), `taint` (reading it taints the session) and `destructive`.
- Decision order: unclassified or blocked → hidden. `ask` with no UI connected → auto-deny. `auto` → run. `ask` → a session grant if one applies, otherwise an approval card.
- Session grants live only in memory. Taint is persisted (`sessions.tainted`), and while a session is tainted it voids both plugin grants and agent-tool grants (Q39).
- Agent-tool grants are scoped by input: Bash by command, Edit/Write by file path, anything else by canonical JSON (Q38).
- If the gateway cannot tie a call to a session ("unknown session"), it treats the call as strictly as possible: it always asks and never offers a session grant.

### Session correlation (기능 명세서 5-2, 아키텍처 7-1)

MCP requests carry no session id, and the `headersHelper` environment does not include one either. The gateway ties a call to a session by matching the `PreToolUse` hook (`mcp__farero__<plugin>_<tool>` plus `tool_input`) that arrives just before the call. Arguments must be compared as canonical JSON (`policy.CanonicalHash`): Claude Code keeps the model's key order in `tool_input` but sends sorted keys over MCP.

`farero-hook` processes, both hook events and `headersHelper`, have the claude process as their parent PID. That makes connection → PID → current session a usable tie-breaker.

### Storage (`internal/store`)

SQLite through `modernc.org/sqlite` (no cgo). Migrations are numbered strings with `PRAGMA user_version`, and the DB file is backed up before a migration runs. `calls_fts` is an FTS5 trigram index. Queries of 2 characters or fewer fall back to `LIKE`, because Korean words are often 2 syllables (Q64). Deleting a session cascades to its hook events, calls and FTS rows. Call results are truncated to 16KB (Q46).

## Facts verified in M0, M2 and M3 that constrain the code

- Without a per-server `timeout` in the MCP server entry, Claude Code gives up on a tool call after about 60 s. Register the gateway with `timeout: 660000` so a 10-minute approval can finish. Hook entries use `timeout: 660`.
- `claude mcp add-json farero '<json>' --scope user` accepts `headersHelper` and `timeout`, so Claude Code writes its own `~/.claude.json` and farero does not have to edit that file by hand.
- Claude Code 2.1.287 first tries `server/discover` (2026-07-28) and then falls back to `initialize` (2025-11-25). The gateway must serve both, which the Go SDK does.
- `PermissionRequest` does not fire for tools matched by `permissions.allow` (`mcp__farero__*`). It fires after `PreToolUse`.
- Items written by `zalando/go-keyring` can be read by any process through `/usr/bin/security` without a prompt. So `secret` uses the Security framework (cgo), and only `farerod` touches Keychain. The hook gets the gateway secret over the socket (`headers.issue`).
- Other apps can register their own `PermissionRequest` hooks in user settings, and their answer can override farero's.
- `PermissionRequest` has no `tool_use_id`; the `PreToolUse` just before it (same tool and input) and the tool's `PostToolUse(Failure)` do. When the user allows at the terminal first, Claude Code ignores the hook's later answer, so a farero deny can be followed by the tool running (`internal/core/agentwait.go` corrects the log row). Esc while an allowed tool runs is recorded by Claude Code as a rejection and sends no hook event either.
- Esc, Ctrl-C and "No" at the terminal's permission prompt send no hook event. Claude Code only writes `[Request interrupted by user…]` into the transcript, and "No"/Esc also kills a waiting `PermissionRequest` hook. "Yes" in the terminal does not end the hook: the tool runs and `PostToolUse` arrives while the hook still waits (`internal/core/agentwait.go`, `session.CheckInterrupts`).
- `Notification` carries `notification_type` (`permission_prompt` about 6 s after an unanswered `PermissionRequest`, `idle_prompt` 60 s after `Stop`). An API error ends the turn with `StopFailure` instead of `Stop`.
- Internal forks (compaction, prompt suggestions) send `PreToolUse` with `agent_id` but no `agent_type` key, for tools that never run. Real subagents carry both and share the parent's `session_id`.
- Claude Code before 2.1.101 ignores the whole `settings.json` if it has a hook event name it does not know, so farero refuses to register with a Claude Code older than `MinClaudeVersion`.
- A hook still running after claude exited is adopted by launchd: its parent PID is 1.

## Working rules for this repo

- Never modify the developer's real Claude Code config (`~/.claude/settings.json`, `~/.claude.json`) while testing. Use `claude -p --settings <file> --mcp-config <file> --strict-mcp-config --setting-sources project`, or `CLAUDE_CONFIG_DIR=<tmp>` for `claude mcp` commands.
- Point `FARERO_HOME` / `FARERO_SOCKET` somewhere else in dev and test runs. Keep socket paths short (under 104 bytes on macOS), for example under `/tmp`.
- Do not use the real Keychain in tests. With ad-hoc signing, every rebuild changes the code signature and triggers a Keychain prompt.
- Git: this repo is `farero-dev/farero-dev.github.io`. GitHub Pages serves `main` from the repo root (legacy Jekyll build). Push work to `develop` (`git push origin HEAD:develop`). Merging into `main` happens later, only when the user asks.



Custom Instructions

- answer word only use korean
- 만약 대기가 필요한 작업이 있다면 서브 에이전트 이용
- 병렬로 처리할 작업이 있다면 서브 에이전트 이용
