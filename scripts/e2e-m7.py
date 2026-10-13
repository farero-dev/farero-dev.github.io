#!/usr/bin/env python3
"""End-to-end check of the finishing features (M7; 기능 명세서 F-11, F-09
search, 6-5, 5-1) with the real app bundle, real Terminal.app windows and a
real, interactive Claude Code.

    scripts/e2e-m7.py

The app under test is build/Farero.app (scripts/build.sh; E2E_APP: another
bundle) in development mode (FARERO_SOCKET set: no LaunchAgent, the installed
/Applications/Farero.app and its farerod are left alone). It is the only UI
that answers cards; `farero-devctl watch` only observes. farerod is a dev
build (the fake `dev` plugin) with its own FARERO_HOME (E2E_DIR, default
/tmp/frm7) and an isolated Claude Code registration (scripts/e2elib.py).

Terminal jump: alpha and beta are Claude Code sessions started in their own
new Terminal.app windows (`do script`); gamma runs in a pseudo-terminal of
this script, so its tty belongs to no Terminal.app tab. The script cannot
click the notch (posting input needs Accessibility); `jump <session>` on the
app's debug control pipe (FARERO_DEBUG_CONTROL) runs the code a tap on the
session row runs. The windows the script opened are closed at the end.

Automation permission: started straight from Contents/MacOS, the app's Apple
Events seem to count as the ones of the app that started this script (the
terminal running it; no prompt appears), which then needs Automation access
to Terminal. E2E_MANUAL=1 starts it with `open` as an app of its own instead,
so the first jump shows macOS's prompt for Farero
(NSAppleEventsUsageDescription), and asks the person at the Mac to answer
it, to click the session rows in the notch for the Terminal jumps, and to
press the shortcuts for the cards (as scripts/e2e-notch.py does).

What it checks (MVP 구현 순서 M7 완료 기준: 시나리오 E, 한국어 2글자 검색어):
  1. Scenario E: with a third Terminal window in front, jumping to alpha
     brings the window whose selected tab has alpha's tty to the front and
     makes Terminal the frontmost app; jumping to beta does the same for
     beta; a minimized window comes back. Jumping to gamma shows
     "터미널 창을 찾을 수 없음". Farero itself never becomes active.
  2. Log search: gamma's Bash `echo 배포 완료 … >> deploy.log` (answered in the notch) and
     dev_echo rows are found with 1- and 2-character Korean queries (LIKE)
     and longer ones (FTS5 trigram), through farerod's log.query and through
     the app's log-search code; the filters agent, kind/plugin, tool,
     decision, period and session narrow the result.
  3. Settings: dev write_sim set to auto runs without a card and keeps its
     level across a farerod restart; back to the default it asks again;
     destroy_sim (approve every time) cannot be made auto. The result limit
     set to 4 KB stores a 6 KB dev_echo result as at most 4 KB (valid UTF-8)
     with result_bytes 6 KB; limits outside 1 KB-1 MB are refused; changing
     one setting leaves the other alone.
  4. Session delete: deleting gamma removes its hook events, calls and search
     hits; gamma was tainted, keeps running, and comes back tainted with its
     next call logged.

Environment: E2E_DIR (default /tmp/frm7), E2E_MODEL (default haiku),
E2E_NO_BUILD=1 to reuse build/dev and build/Farero.app, E2E_APP,
E2E_MANUAL=1. Exit status is non-zero when a check fails.
"""
import importlib.util
import json
import os
import re
import shlex
import signal
import socket
import sqlite3
import subprocess
import sys
import time
from datetime import datetime

from e2elib import BIN, CLAUDE, MODEL, ROOT, Abort, Agent, Harness, check_run_dir, gateway_headers, http, \
    print_daemon_warnings, ready, rpc_result, step_register, step_remove


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), os.path.join(ROOT, "scripts", name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


N = load("e2e-notch")  # App (the bundle under test), probe, answer
A = load("e2e-approvals")  # Bash prompts and their audit rows
G = load("e2e-gateway")  # dev plugin prompts and rows

E = os.environ.get("E2E_DIR", "/tmp/frm7")
WORK = os.path.join(ROOT, "build", "e2e-m7")
APP = N.APP
MANUAL = os.environ.get("E2E_MANUAL") == "1"
NOT_FOUND = "터미널 창을 찾을 수 없음"
TERMINAL = "com.apple.Terminal"

FACTS = []


def fact(h, label, text):
    FACTS.append((label, text))
    h.note(f"fact {label}: {text}")


def prepare():
    check_run_dir(E)
    for name in ("alpha", "beta", "gamma"):
        subprocess.run(["rm", "-rf", os.path.join(WORK, name)])
        os.makedirs(os.path.join(WORK, name))
    subprocess.run(["rm", "-rf", E])
    os.makedirs(os.path.join(E, "claude"))
    if os.environ.get("E2E_NO_BUILD") != "1":
        print("==> build dev binaries and Farero.app", flush=True)
        if subprocess.run(["go", "build", "-p", "2", "-tags", "farero_dev", "-o", BIN + "/", "./cmd/..."],
                          cwd=os.path.join(ROOT, "daemon")).returncode != 0:
            return False
        if not os.environ.get("E2E_APP") and subprocess.run(
                [os.path.join(ROOT, "scripts", "build.sh"), "0.0.0-e2e"], cwd=ROOT).returncode != 0:
            return False
    if not os.path.exists(N.PROBE) or os.path.getmtime(N.PROBE) < os.path.getmtime(
            os.path.join(ROOT, "scripts", "notchprobe.swift")):
        if subprocess.run(["swiftc", "-O", "-o", N.PROBE, os.path.join(ROOT, "scripts", "notchprobe.swift")]).returncode:
            return False
    return os.path.exists(os.path.join(APP, "Contents", "MacOS", "Farero"))


# ---------------------------------------------------------------------------
# The app: straight from Contents/MacOS, or (E2E_MANUAL=1) through `open`


class _Opened:
    """Enough of Popen for N.App: an app started by `open` (LaunchServices),
    so macOS treats it as an app of its own for Automation."""

    def __init__(self, pid):
        self.pid = pid

    def poll(self):
        try:
            os.kill(self.pid, 0)
            return None
        except ProcessLookupError:
            return 0

    def terminate(self):
        try:
            os.kill(self.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass

    def kill(self):
        try:
            os.kill(self.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass

    def wait(self, timeout=None):
        end = time.time() + (timeout or 3600)
        while self.poll() is None:
            if time.time() > end:
                raise subprocess.TimeoutExpired("Farero", timeout)
            time.sleep(0.1)
        return 0


def start_app(h):
    if not MANUAL:
        return N.App(h)
    app = N.App.__new__(N.App)
    app.h = h
    app.log_path = os.path.join(h.e, "app.log")
    app.ctl = os.path.join(h.e, "app.ctl")
    if not os.path.exists(app.ctl):
        os.mkfifo(app.ctl, 0o600)
    app.start_pos = 0
    exe = os.path.join(APP, "Contents", "MacOS", "Farero")
    before = set(pids_of(exe))
    env = {"FARERO_HOME": h.e, "FARERO_SOCKET": h.env["FARERO_SOCKET"], "FARERO_DEBUG": "1",
           "FARERO_DEBUG_CONTROL": app.ctl}
    cmd = ["open", "-n", "-g", "--stdout", app.log_path, "--stderr", app.log_path]
    for k, v in env.items():
        cmd += ["--env", f"{k}={v}"]
    subprocess.run(cmd + ["-a", APP], check=True)
    pid = h.wait(lambda: next(iter(set(pids_of(exe)) - before), None), 15)
    if not pid:
        raise Abort("Farero.app did not start through open")
    app.proc = _Opened(pid)
    return app


def pids_of(exe):
    out = subprocess.run(["pgrep", "-f", exe], capture_output=True, text=True).stdout
    return [int(p) for p in out.split()]


# ---------------------------------------------------------------------------
# farerod over its socket (like the app) and the gateway over HTTP


def ipc(h, typ, data=None, timeout=15):
    """One request as a UI client; returns (reply type, reply data)."""
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.settimeout(timeout)
    s.connect(h.env["FARERO_SOCKET"])
    f = s.makefile("rwb")
    try:
        def send(t, i, d):
            f.write((json.dumps({"type": t, "id": i, "data": d}, ensure_ascii=False) + "\n").encode())
            f.flush()

        def reply(i):
            while True:
                line = f.readline()
                if not line:
                    raise Abort(f"farerod closed the socket before answering {typ}")
                m = json.loads(line)
                if m.get("id") == i:
                    return m.get("type"), m.get("data")
        send("ui.hello", "hello", {"version": "e2e-m7"})
        reply("hello")
        send(typ, "r", data)
        return reply("r")
    finally:
        f.close()
        s.close()


def search(h, **f):
    f.setdefault("limit", 200)
    typ, rows = ipc(h, "log.query", f)
    if typ != "log.result":
        raise Abort(f"log.query {f}: {typ} {rows}")
    return rows or []


class Gateway:
    """A raw MCP client of the gateway, outside any Claude Code session (its
    calls are "unknown session")."""

    def __init__(self, h):
        srv = json.load(open(os.path.join(h.e, "mcp.json")))["mcpServers"]["farero"]
        self.url = srv["url"]
        self.base = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
                     **gateway_headers(h, srv["headersHelper"])}
        init = {"jsonrpc": "2.0", "id": 1, "method": "initialize",
                "params": {"protocolVersion": "2025-11-25", "capabilities": {},
                           "clientInfo": {"name": "farero-e2e-m7", "version": "1"}}}
        st, rh, body = http(self.url, init, self.base)
        res = (rpc_result(rh, body, 1) or {}).get("result") or {}
        if st != 200 or not res.get("protocolVersion"):
            raise Abort(f"gateway initialize: status {st} {body[:200]!r}")
        sid = rh.get("Mcp-Session-Id")
        self.hdr = dict(self.base, **({"Mcp-Session-Id": sid} if sid else {}),
                        **{"Mcp-Protocol-Version": res["protocolVersion"]})
        http(self.url, {"jsonrpc": "2.0", "method": "notifications/initialized"}, self.hdr)
        self.next_id = 10

    def call(self, name, args, timeout=30):
        self.next_id += 1
        st, rh, body = http(self.url, {"jsonrpc": "2.0", "id": self.next_id, "method": "tools/call",
                                       "params": {"name": name, "arguments": args}}, self.hdr, timeout=timeout)
        return st, (rpc_result(rh, body, self.next_id) or {}).get("result")


def db(h, sql, args=()):
    c = sqlite3.connect(f"file:{os.path.join(h.e, 'farero.db')}?mode=ro", uri=True, timeout=5)
    try:
        return c.execute(sql, args).fetchall()
    finally:
        c.close()


# ---------------------------------------------------------------------------
# Terminal.app


def osa(script, timeout=30):
    p = subprocess.run(["osascript", "-e", script], capture_output=True, text=True, timeout=timeout)
    if p.returncode != 0:
        raise Abort(f"osascript: {p.stderr.strip()}")
    return p.stdout.strip()


def as_string(s):
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


class Terminal:
    """The Terminal.app windows this script opens (and closes again)."""

    def __init__(self, h):
        self.h = h
        self.windows = {}  # name -> (window id, tty without /dev/)
        self.was_running = osa('application id "com.apple.Terminal" is running') == "true"

    def open(self, name, command):
        """A new window running command; returns its tab's tty (ttysNNN)."""
        out = osa(f"""
tell application id "{TERMINAL}"
    set t to do script {as_string(command)}
    delay 0.5
    set wid to missing value
    repeat with w in windows
        repeat with x in tabs of w
            if tty of x is (tty of t) then set wid to id of w
        end repeat
    end repeat
    return (tty of t) & " " & wid
end tell""")
        tty, wid = out.split()
        tty = tty.removeprefix("/dev/")
        self.windows[name] = (int(wid), tty)
        self.h.note(f"Terminal window {name}: id {wid}, tty {tty}")
        return tty

    def front(self):
        """(Terminal frontmost?, tty of the front window's selected tab,
        front window id, miniaturized?)"""
        out = osa(f"""
tell application id "{TERMINAL}"
    set w to front window
    return (frontmost as text) & " " & (tty of selected tab of w) & " " & (id of w) & " " & (miniaturized of w as text)
end tell""")
        fm, tty, wid, mini = out.split()
        return fm == "true", tty.removeprefix("/dev/"), int(wid), mini == "true"

    def miniaturize(self, name):
        wid = self.windows[name][0]
        osa(f'tell application id "{TERMINAL}" to set miniaturized of (first window whose id is {wid}) to true')

    def miniaturized(self, name):
        wid = self.windows[name][0]
        return osa(f'tell application id "{TERMINAL}" to return miniaturized of (first window whose id is {wid})') \
            == "true"

    def type_into(self, name, text):
        wid = self.windows[name][0]
        osa(f'tell application id "{TERMINAL}" to do script {as_string(text)} in selected tab of '
            f'(first window whose id is {wid})')

    def close_all(self):
        for name, (wid, _) in list(self.windows.items()):
            try:
                osa(f'tell application id "{TERMINAL}" to close (every window whose id is {wid})')
            except Abort as ex:
                self.h.note(f"closing Terminal window {name}: {ex}")
        self.windows.clear()


def agent_command(h, cwd):
    """The shell command a Terminal window runs: claude with the isolated
    registration, like e2elib.Agent. Terminal.app may itself carry the
    environment of a Claude Code session (if one started it); the CLAUDE*
    variables are removed (CLAUDE_CODE_CHILD_SESSION turns transcripts off)."""
    args = [CLAUDE, "--model", MODEL, "--setting-sources", "project", "--settings",
            os.path.join(h.e, "claude", "settings.json"), "--strict-mcp-config", "--mcp-config",
            os.path.join(h.e, "mcp.json"), "--permission-mode", "default"]
    env = {"FARERO_HOME": h.e, "FARERO_SOCKET": h.env["FARERO_SOCKET"], "DISABLE_AUTOUPDATER": "1"}
    unset = "$(env | sed -n 's/^\\(CLAUDE[A-Za-z0-9_]*\\)=.*/-u \\1/p')"
    return (f"cd {shlex.quote(cwd)} && exec env {unset} "
            + " ".join(f"{k}={shlex.quote(v)}" for k, v in env.items()) + " "
            + " ".join(shlex.quote(a) for a in args))


class TermAgent:
    """A Claude Code session in a Terminal.app window."""

    def __init__(self, name, sid, pid, tty):
        self.name, self.sid, self.pid, self.tty = name, sid, pid, tty


def start_terminal_agent(h, term, name):
    since = time.time()
    tty = term.open(name, agent_command(h, os.path.join(WORK, name)))
    ev = h.wait(lambda: h.find_event("session.updated", lambda d: d.get("tty") == tty, since), 90)
    if not h.check(ev, f"{name}: claude in Terminal.app, SessionStart reached farerod with the tab's tty {tty}"):
        raise Abort(f"{name} did not start in Terminal")
    d = ev["data"]
    h.check(d.get("pid") and d.get("cwd", "").endswith(name), f"{name}: session with its pid and folder",
            json.dumps({k: d.get(k) for k in ("id", "pid", "tty", "cwd")}))
    return TermAgent(name, d["id"], d.get("pid"), tty)


def start_pty_agent(h, name):
    since = time.time()
    a = Agent(h, name)
    a.started = since
    h.agents.append(a)
    ev = h.wait(lambda: h.find_event("session.updated", lambda d: d.get("pid") == a.proc.pid, since), 60)
    ok = ev and h.wait(lambda: ready(a), 30)
    a.save_screen("started")
    if not h.check(ok, f"{a.name}: started in a pseudo-terminal ({a.tty}), SessionStart reached farerod"):
        raise Abort(f"{a.name} did not start")
    a.sid = ev["data"]["id"]
    h.check(ev["data"].get("tty") == a.tty, f"{a.name}: session tty is the pseudo-terminal",
            f"{ev['data'].get('tty')} vs {a.tty}")
    h.sleep(1.0)
    return a


# ---------------------------------------------------------------------------
# Steps


def jump(h, app, sid, click=None, timeout=10):
    """Jumps to sid's terminal: `jump <sid>` on the control pipe, or (E2E_MANUAL
    and click, the session's folder name) the person clicks the session row
    in the notch. Returns (outcome, tty) from the app's log. The first jump
    in E2E_MANUAL mode may wait for macOS's Automation prompt."""
    pos = app.pos()
    if MANUAL and click:
        print(f"  >>> point at the notch to open it and click the session row {click!r}"
              " (if macOS asks whether Farero may control Terminal, click 허용/OK). Waiting up to 2 minutes",
              flush=True)
        timeout = 120
    else:
        app.send(f"jump {sid}")
        if MANUAL and not app.find(r"farero: jump ", 0):
            print("  >>> macOS may ask whether Farero may control Terminal: click 허용 (OK). Waiting up to 2 minutes",
                  flush=True)
            timeout = 120
    m = app.wait(rf"farero: jump {re.escape(sid)} tty=(\S*): (jumped|notFound|failed.*)$", pos, timeout)
    if not m:
        return None, None
    if MANUAL and click:
        h.check(not app.find(rf"farero: control jump {re.escape(sid)}", pos),
                f"the jump to {click} came from a click in the notch, not the control pipe")
    return m[2], m[1]


def to_background(h, app, back):
    """Brings `back` (the app that was in front when the step began, not
    Terminal) forward again, so the jump starts with Terminal in the
    background, as it does when the person works in another app."""
    if not back:
        return
    subprocess.run(["open", "-b", back], capture_output=True)
    f = h.wait(lambda: (lambda f: f if f.get("bundle") == back else None)(N.probe("front", app.proc.pid) or {}), 5)
    h.check(f, f"{back} is in front again (Terminal in the background)",
            json.dumps(N.probe("front", app.proc.pid) or {}, ensure_ascii=False))


def check_jumped(h, app, term, who, focus_pid, back=None):
    to_background(h, app, back)
    t0 = time.time()
    outcome, tty = jump(h, app, who.sid, click=who.name)
    if outcome and outcome.startswith("failed") and "권한" in outcome:
        h.check(False, f"jump to {who.name}: Automation permission for Terminal",
                f"{outcome} — allow it in System Settings > Privacy & Security > Automation "
                f"({'Farero' if MANUAL else 'the app running this script'} > Terminal)")
        return False
    h.check(outcome == "jumped", f"jump to {who.name}: jumped", f"outcome {outcome!r} tty {tty!r}")
    st = h.wait(lambda: (lambda f: f if f[0] and f[1] == who.tty else None)(term.front()), 5) or term.front()
    h.check(st[0] and st[1] == who.tty and st[2] == term.windows[who.name][0] and not st[3],
            f"Terminal is frontmost, its front window is {who.name}'s (selected tab {who.tty}), not minimized",
            f"frontmost={st[0]} tab={st[1]} window={st[2]} (want {term.windows[who.name][0]}) miniaturized={st[3]}")
    f = N.probe("front", focus_pid) or {}
    h.check(f.get("bundle") == TERMINAL and not f.get("target_active"),
            "the frontmost app is Terminal (NSWorkspace) and Farero is not active", json.dumps(f, ensure_ascii=False))
    fact(h, f"jump {who.name}", f"{outcome} in {time.time() - t0:.2f} s (tty {who.tty})")
    return True


def step_scenario_e(h, app, term, alpha, beta, gamma):
    h.section("scenario E: jump from the notch to the session's Terminal.app window")
    before = N.probe("front", app.proc.pid) or {}
    h.note(f"frontmost app before: {before.get('name')!r} ({before.get('bundle')})")
    back = before.get("bundle") if before.get("bundle") not in (None, "", TERMINAL) else None
    if not back:
        h.note("Terminal was in front already: the jumps start with Terminal active")
    other = term.open("plain", "")
    st = term.front()
    h.check(st[1] == other, "a third Terminal window (a plain shell) is Terminal's front window",
            f"front tab {st[1]}, plain {other}")
    if not check_jumped(h, app, term, alpha, app.proc.pid, back):
        return
    check_jumped(h, app, term, beta, app.proc.pid, back)
    h.check(term.front()[1] != alpha.tty, "beta's window is now in front of alpha's")
    step_minimized(h, app, term, alpha)
    step_not_found(h, app, term, alpha, gamma)


def step_minimized(h, app, term, alpha):
    h.section("scenario E: a minimized window comes back")
    term.miniaturize("alpha")
    if not h.wait(lambda: term.miniaturized("alpha"), 5):
        # Seen on macOS 26 / Terminal 2.15 while writing this: the property
        # stays false. Then there is nothing to bring back.
        h.note("Terminal did not minimize alpha's window (`set miniaturized` had no effect): not checked")
        return
    h.check(True, "alpha's window is minimized")
    h.sleep(1.0)  # the minimize animation
    check_jumped(h, app, term, alpha, app.proc.pid)
    h.check(not term.miniaturized("alpha"), "alpha's window is no longer minimized")


def step_not_found(h, app, term, alpha, gamma):
    h.section("scenario E: a session outside Terminal.app -> 터미널 창을 찾을 수 없음")
    front = term.front()[1]
    pos = app.pos()
    outcome, tty = jump(h, app, gamma.sid)
    h.check(outcome == "notFound" and tty == gamma.tty, "jump to gamma (a pseudo-terminal): notFound",
            f"outcome {outcome!r} tty {tty!r}")
    h.check(app.wait(rf"farero: toast {NOT_FOUND}$", pos, 5), f"the notch shows {NOT_FOUND!r}")
    h.check(term.front()[1] == front, "Terminal's front window did not change")

    h.section("scenario E: an unknown session id is ignored")
    pos = app.pos()
    app.send("jump claude:no-such-session")
    h.check(app.wait(r"farero: control jump claude:no-such-session: ignored", pos, 5), "ignored")


def ask_dev(h, a, tool, text):
    since = G.ask_tool(h, a, tool, text)
    return since


def step_log_data(h, app, gamma):
    h.section("log data: gamma's Bash `echo 배포 완료 … >> deploy.log` (answered in the notch) and dev_echo")
    # A plain echo needs no permission in Claude Code 2.1.296; writing a file does.
    cmd = "echo 배포 완료 m7-bash >> deploy.log"
    card, _, since = A.ask(h, gamma, cmd)
    if not card:
        raise Abort("no card for the Bash command")
    N.answer(h, app, "allow")
    row = A.wait_row(h, cmd, since)
    h.check(row and row["decision"] == "user_allowed", "Bash row: user_allowed", row and row["decision"])
    G.turn_end(h, gamma, since)

    since = ask_dev(h, gamma, "echo", "배포 완료 확인 m7-echo")
    row = G.wait_row(h, "echo", "배포 완료 확인 m7-echo", since)
    h.check(row and row["decision"] == "auto_allowed" and row["session_id"] == gamma.sid,
            "dev_echo row: auto_allowed, gamma's session", G.row_desc(row))
    G.turn_end(h, gamma, since)


def ids(rows):
    return sorted(r["id"] for r in rows)


def step_search(h, app, gamma, t0):
    h.section("log search: Korean 1-2 characters (LIKE) and longer (FTS5 trigram), farerod log.query")
    bash = [r for r in search(h, kind="agent", tool="Bash") if "배포 완료" in json.dumps(r.get("input"),
                                                                                     ensure_ascii=False)]
    echo = [r for r in search(h, kind="plugin", plugin="dev", tool="echo")
            if "배포 완료 확인" in json.dumps(r.get("input"), ensure_ascii=False)]
    if not h.check(len(bash) == 1 and len(echo) == 1, "one Bash row and one dev_echo row to look for",
                   f"bash {len(bash)} echo {len(echo)}"):
        return
    b, e = bash[0]["id"], echo[0]["id"]
    both = sorted([b, e])
    for q, want, how in (("배", both, "1 character (LIKE)"), ("배포", both, "2 characters (LIKE)"),
                         ("완료", both, "2 characters (LIKE)"), ("배포 완료", both, "5 characters (trigram)"),
                         ("완료 확인", [e], "the dev_echo text only (trigram)"),
                         ("m7-bash", [b], "ASCII (trigram)"), ("없는검색어", [], "no match")):
        got = [i for i in ids(search(h, query=q)) if i in both]
        h.check(got == want, f"query {q!r} ({how}): {len(want)} row(s)", f"got {got}, want {want}")

    h.section("log search: filters (agent, kind/plugin, tool, decision, period, session)")
    t0_iso = datetime.fromtimestamp(t0 - 1).astimezone().isoformat()
    end_iso = datetime.fromtimestamp(time.time() + 60).astimezone().isoformat()
    cases = (({"agent": "claude"}, both), ({"agent": "codex"}, []),
             ({"kind": "agent"}, [b]), ({"kind": "plugin", "plugin": "dev"}, [e]), ({"plugin": "railway"}, []),
             ({"tool": "Bash"}, [b]), ({"tool": "echo"}, [e]),
             ({"decision": "user_allowed"}, [b]), ({"decision": "auto_allowed"}, [e]), ({"decision": "denied"}, []),
             ({"from": t0_iso, "to": end_iso}, both), ({"to": t0_iso}, []),
             ({"session_id": gamma.sid}, both), ({"session_id": "claude:other"}, []))
    for f, want in cases:
        got = [i for i in ids(search(h, query="배포", **f)) if i in both]
        h.check(got == want, f"'배포' + {json.dumps(f, ensure_ascii=False)}: {len(want)} row(s)",
                f"got {got}, want {want}")

    h.section("log search through the app's log-search code (logsearch on the control pipe)")
    for q, mode in (("배", "부분 일치"), ("배포", "부분 일치"), ("배포 완료", "전문 검색")):
        pos = app.pos()
        app.send(f"logsearch {q}")
        m = app.wait(rf'farero: logsearch "{re.escape(q)}" (?:mode=(.+?) results=(\d+)|error (.*))$', pos, 10)
        h.check(m and m[1] == mode and int(m[2]) >= 2, f"app: {q!r} -> {mode}, at least the two rows",
                m and m[0])


def step_settings_policy(h, app, gamma):
    h.section("settings: tool level (6-5) — dev write_sim auto runs without a card")
    typ, st = h.devctl("policy", "set", "dev", "write_sim", "auto")
    row = next((t for t in st or [] if t.get("plugin") == "dev" and t.get("tool") == "write_sim"), None)
    h.check(typ == "policy.state" and row and row["level"] == "auto" and row["overridden"]
            and row["default_level"] == "ask", "policy.set: write_sim auto, overridden, default ask", json.dumps(row))
    since = ask_dev(h, gamma, "write_sim", "m7-auto")
    r = G.wait_row(h, "write_sim", "m7-auto", since)
    h.check(r and r["decision"] == "auto_allowed", "write_sim ran: auto_allowed", G.row_desc(r))
    h.check(not h.find_event("approval.request", lambda d: d.get("tool") == "write_sim", since), "no card for it")
    G.turn_end(h, gamma, since)

    h.section("settings: an approve-every-time tool cannot be auto (Q33)")
    typ, err = h.devctl("policy", "set", "dev", "destroy_sim", "auto")
    h.check(typ == "error", "policy.set destroy_sim auto: refused", f"{typ} {err}")
    _, st = h.devctl("policy")
    d = next((t for t in st or [] if t.get("tool") == "destroy_sim"), {})
    h.check(d.get("level") == "ask" and d.get("no_session") and not d.get("overridden"),
            "destroy_sim stays ask / approve every time", json.dumps(d))

    h.section("settings: the level survives a farerod restart")
    pos = app.pos()
    h.stop_watch()
    h.stop_daemon()
    h.start_daemon(append=True)
    h.start_watch()
    h.check(app.wait(r"farero: recv state\.snapshot", pos, 20), "the app reconnected")
    _, st = h.devctl("policy")
    w = next((t for t in st or [] if t.get("tool") == "write_sim"), {})
    h.check(w.get("level") == "auto" and w.get("overridden"), "write_sim is still auto after the restart",
            json.dumps(w))

    h.section("settings: back to the default -> asks again")
    typ, st = h.devctl("policy", "set", "dev", "write_sim")
    w = next((t for t in st or [] if t.get("tool") == "write_sim"), {})
    h.check(w.get("level") == "ask" and not w.get("overridden"), "write_sim reset to ask", json.dumps(w))
    h.wait(lambda: ready(gamma), 30)
    since = ask_dev(h, gamma, "write_sim", "m7-ask")
    card, _ = G.wait_plugin_card(h, "write_sim", "m7-ask", since, 90)
    if card:
        N.answer(h, app, "deny")
        r = G.wait_row(h, "write_sim", "m7-ask", since)
        h.check(r and r["decision"] == "denied", "the card came back; denied in the notch", G.row_desc(r))
    G.turn_end(h, gamma, since)


def step_settings_limit(h):
    h.section("settings: result size limit (F-09, Q46)")
    typ, s = ipc(h, "settings.get")
    h.check(typ == "settings.get" and s.get("result_limit_bytes") == 16384 and s.get("update_check") is True,
            "defaults: 16 KB, update check on", json.dumps(s))
    typ, s = ipc(h, "settings.set", {"result_limit_bytes": 4096})
    h.check(s and s.get("result_limit_bytes") == 4096 and s.get("update_check") is True,
            "limit 4 KB; update check left on", f"{typ} {json.dumps(s)}")
    for bad in (512, 2 << 20):
        typ, err = ipc(h, "settings.set", {"result_limit_bytes": bad})
        h.check(typ == "error", f"limit {bad} bytes: refused (1 KB-1 MB)", f"{typ} {err}")
    typ, s = ipc(h, "settings.set", {"update_check": False})
    h.check(s and s.get("result_limit_bytes") == 4096 and s.get("update_check") is False,
            "update check off; limit left at 4 KB", f"{typ} {json.dumps(s)}")

    text = "가" * 2000  # 6000 bytes of UTF-8
    t0 = time.time()
    st, res = Gateway(h).call("dev_echo", {"text": text})
    h.check(st == 200 and res and not res.get("isError"), "dev_echo with a 6 KB text answered", f"status {st}")
    rows = h.wait(lambda: db(h, "SELECT id, result_bytes, CAST(result_text AS BLOB) FROM calls WHERE tool = 'echo' "
                                "AND ts >= ? ORDER BY id DESC", (int(t0 * 1000) - 1000,)), 10)
    if h.check(rows, "its audit row"):
        _, n, blob = rows[0]
        try:
            blob.decode("utf-8")
            valid = True
        except UnicodeDecodeError:
            valid = False
        h.check(n == 6000 and 0 < len(blob) <= 4096 and valid,
                "stored: at most 4096 bytes, valid UTF-8; result_bytes 6000", f"result_bytes {n}, stored {len(blob)}")
        fact(h, "limit", f"6000-byte result stored as {len(blob)} bytes with the 4 KB limit")
    typ, s = ipc(h, "settings.set", {"result_limit_bytes": 16384, "update_check": True})
    h.check(s and s.get("result_limit_bytes") == 16384 and s.get("update_check") is True, "restored the defaults")


def step_delete(h, app, gamma):
    h.section("session delete: gamma is tainted, then deleted while it keeps running")
    since = ask_dev(h, gamma, "taint_sim", "m7-taint")
    ev = h.wait_event("session.updated", lambda d: d.get("id") == gamma.sid and d.get("tainted"), since, 90)
    h.check(ev, "gamma is tainted (taint_sim)")
    G.turn_end(h, gamma, since)
    before = db(h, "SELECT COUNT(*) FROM calls WHERE session_id = ?", (gamma.sid,))[0][0]
    h.check(before >= 3, f"gamma has {before} audit rows before the delete")

    pos = app.pos()
    t_del = time.time()
    typ, _ = ipc(h, "session.delete", {"session_id": gamma.sid})
    h.check(typ == "ok", "session.delete: ok", typ)
    # The watch log's times are cut to the millisecond, and the delete can
    # reach it within one.
    h.check(h.wait_event("session.removed", lambda d: d.get("session_id") == gamma.sid, t_del - 1, 5),
            "session.removed sent to the UIs")
    h.check(app.wait(r"farero: recv session\.removed", pos, 5), "the app received session.removed")
    h.check(db(h, "SELECT COUNT(*) FROM sessions WHERE id = ?", (gamma.sid,))[0][0] == 0, "no sessions row")
    h.check(db(h, "SELECT COUNT(*) FROM hook_events WHERE session_id = ?", (gamma.sid,))[0][0] == 0,
            "no hook events")
    h.check(db(h, "SELECT COUNT(*) FROM calls WHERE session_id = ?", (gamma.sid,))[0][0] == 0, "no audit rows")
    for q in ("배포", "배포 완료", "m7-auto"):
        rows = search(h, query=q)
        h.check(rows == [], f"search {q!r}: nothing (the FTS rows went with them)", f"{len(rows)} rows")
    pos = app.pos()
    app.send("logsearch 배포 완료")
    m = app.wait(r'farero: logsearch "배포 완료" mode=(.+?) results=(\d+)$', pos, 10)
    h.check(m and m[2] == "0", "the app's search: 0 rows", m and m[0])

    h.section("session delete: the live session comes back tainted (Q39)")
    since = ask_dev(h, gamma, "echo", "m7-after-delete")
    ev = h.wait_event("session.updated", lambda d: d.get("id") == gamma.sid, since, 90)
    h.check(ev and ev["data"].get("tainted"), "gamma is back, still tainted", ev and json.dumps(ev["data"])[:300])
    r = G.wait_row(h, "echo", "m7-after-delete", since)
    h.check(r and r["session_id"] == gamma.sid, "its next call is logged under gamma", G.row_desc(r))
    G.turn_end(h, gamma, since)


def stop_terminal_agents(h, term, agents):
    for a in agents:
        if a and a.pid:
            try:
                os.kill(a.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
    for a in agents:
        if a and a.pid:
            h.wait(lambda: subprocess.run(["kill", "-0", str(a.pid)], capture_output=True).returncode != 0, 10)
    h.sleep(1.0)
    term.close_all()


def main():
    if not prepare():
        print("==> E2E M7 FAILED (build)")
        return 1
    daemon_env = {"FARERO_APPROVAL_TIMEOUT": "10m" if MANUAL else "2m"}
    h = Harness(E, WORK, daemon_env)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(130))
    app = term = None
    alpha = beta = None
    t0 = time.time()
    try:
        h.start_daemon()
        h.start_watch()
        step_register(h)
        app = start_app(h)
        h.check(app.wait(r"farero: recv state\.snapshot", timeout=15), "the app connected (state.snapshot)")
        term = Terminal(h)
        h.section("sessions: alpha and beta in Terminal.app windows, gamma in a pseudo-terminal")
        alpha = start_terminal_agent(h, term, "alpha")
        beta = start_terminal_agent(h, term, "beta")
        gamma = start_pty_agent(h, "gamma")
        step_scenario_e(h, app, term, alpha, beta, gamma)
        step_log_data(h, app, gamma)
        step_search(h, app, gamma, t0)
        step_settings_policy(h, app, gamma)
        step_settings_limit(h)
        step_delete(h, app, gamma)
        h.section("the app never became active")
        h.check(not any("app became active" in l for l in app.lines(0)), "no 'app became active' in app.log")
        step_remove(h)
    except Abort as ex:
        print(f"  FAIL {ex}")
        h.failed = True
        h.diag()
    except KeyboardInterrupt:
        print("  interrupted")
        h.failed = True
    finally:
        for a in h.agents:
            a.save_screen("final")
        if term:
            stop_terminal_agents(h, term, (alpha, beta))
        if app:
            app.stop()
        h.cleanup()
    print_daemon_warnings(E)
    if FACTS:
        print("==> facts")
        for label, text in FACTS:
            print(f"  {label}: {text}")
    print(f"==> E2E M7 {'FAILED' if h.failed else 'PASSED'} (logs in {E})")
    return 1 if h.failed else 0


if __name__ == "__main__":
    sys.exit(main())
