#!/usr/bin/env python3
"""End-to-end check of session watching (M2) with a real, interactive Claude Code.

    scripts/e2e-sessions.py

Two `claude` sessions (folders `alpha` and `beta` under build/e2e-sessions)
run at the same time (a third, `gamma`, only starts and is killed) in pseudo-terminals owned by this script: headless, no
Terminal window, no focus stealing. The script types into them like a user
(prompts, Esc, the terminal permission prompt, /exit) and checks what a
development farerod reports through `farero-devctl watch` (a stand-in for the
app), its audit log and its database.

Isolation is the same as scripts/e2e.sh: FARERO_HOME/FARERO_SOCKET point at
/tmp (E2E_DIR, default /tmp/frm2), and farero is registered into a throwaway
Claude config dir (FARERO_CLAUDE_CONFIG_DIR=<dir>/claude) whose generated
settings.json and MCP entry are passed to `claude` with --settings /
--mcp-config --strict-mcp-config --setting-sources project. The developer's
real ~/.claude settings and MCP servers are never touched; the parent Claude
Code session's CLAUDE*/MCP* variables are removed from claude's environment.

What it checks (M2 완료 기준):
  1. `agentcfg apply` installs 10 hook events (incl. StopFailure, timeout 660),
     the allow rule and the MCP server without disturbing user settings.
  2. Two simultaneous sessions are told apart (cwd, agent, start, pid, tty)
     and alternate running -> waiting_input.
  3. Esc during a running tool (no hook event) -> waiting_input.
  4. "No" at the terminal permission prompt (app connected) -> card withdrawn,
     waiting_input, log row `cancelled` with an empty reason. The
     permission_prompt Notification does not end waiting_approval.
  5. "Yes" at the terminal prompt -> card withdrawn, the tool runs, log row
     `cancelled`/`answered_in_agent`, waiting_input after Stop.
  6. /exit while a PermissionRequest hook still waits (a terminal-approved
     tool is running): card withdrawn, then ended or unknown (whichever
     Claude Code's SessionEnd allows) within 10 s; a plain /exit -> ended; an
     agent killed without SessionEnd (kill -9, folder `gamma`) -> unknown.
  7. `agentcfg remove` restores settings.json byte for byte.

Environment: E2E_DIR (default /tmp/frm2; keep it short, the socket path must
stay under 104 bytes), E2E_MODEL (default haiku), E2E_NO_BUILD=1 to reuse
build/dev. Uses python3's standard library only. Exit status is non-zero when
a check fails; logs, pty transcripts and screens are left in E2E_DIR.
"""
import codecs
import fcntl
import json
import os
import re
import select
import signal
import shutil
import sqlite3
import struct
import subprocess
import sys
import termios
import time
import unicodedata

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BIN = os.path.join(ROOT, "build", "dev")
E = os.environ.get("E2E_DIR", "/tmp/frm2")
MODEL = os.environ.get("E2E_MODEL", "haiku")
WORK = os.path.join(ROOT, "build", "e2e-sessions")
CLAUDE = os.environ.get("E2E_CLAUDE") or shutil.which("claude") or os.path.expanduser("~/.local/bin/claude")
COLS, ROWS = 120, 40

# The hook events farero registers (기능 명세서 F-02).
HOOK_EVENTS = ["SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure",
               "PermissionRequest", "Notification", "Stop", "StopFailure", "SessionEnd"]

ESC, ENTER, DOWN = b"\x1b", b"\r", b"\x1b[B"


class Abort(Exception):
    """A step cannot go on (its precondition failed)."""


# ---------------------------------------------------------------------------
# A small VT100 screen. Claude Code redraws only the cells that change, so
# its raw output cannot be searched as text; this keeps the visible screen.


class Screen:
    def __init__(self, cols, rows):
        self.cols, self.rows = cols, rows
        self._dec = codecs.getincrementaldecoder("utf-8")("replace")
        self._st = 0  # 0 text, 1 ESC, 2 CSI, 3 string (OSC/DCS), 4 ESC in string, 5 charset
        self._seq = []
        self.reset()

    def reset(self):
        self.buf = [[" "] * self.cols for _ in range(self.rows)]
        self.x = self.y = 0
        self.top, self.bot = 0, self.rows - 1
        self.saved = (0, 0)

    def text(self):
        return "\n".join("".join(r).rstrip() for r in self.buf)

    def feed(self, data):
        for ch in self._dec.decode(data):
            st = self._st
            if st == 0:
                o = ord(ch)
                if o >= 0x20 and o != 0x7F:
                    self._put(ch)
                elif ch == "\x1b":
                    self._st = 1
                elif ch == "\r":
                    self.x = 0
                elif ch in "\n\x0b\x0c":
                    self._lf()
                elif ch == "\b":
                    self.x = max(0, min(self.x, self.cols - 1) - 1)
                elif ch == "\t":
                    self.x = min(self.cols - 1, (self.x // 8 + 1) * 8)
            elif st == 1:
                self._esc(ch)
            elif st == 2:
                if "\x40" <= ch <= "\x7e":
                    self._st = 0
                    self._csi("".join(self._seq), ch)
                elif ch == "\x1b":
                    self._st = 1
                else:
                    self._seq.append(ch)
            elif st == 3:
                if ch == "\x07":
                    self._st = 0
                elif ch == "\x1b":
                    self._st = 4
            elif st == 4:
                if ch == "\\":
                    self._st = 0
                else:
                    self._esc(ch)
            else:  # charset designation: one more character
                self._st = 0

    def _esc(self, ch):
        self._st = 0
        if ch == "[":
            self._st, self._seq = 2, []
        elif ch in "]P_^X":
            self._st = 3
        elif ch in "()*+-./#%":
            self._st = 5
        elif ch == "7":
            self.saved = (self.x, self.y)
        elif ch == "8":
            self.x, self.y = self.saved
        elif ch == "D":
            self._lf()
        elif ch == "E":
            self.x = 0
            self._lf()
        elif ch == "M":
            if self.y == self.top:
                self._scroll(-1)
            elif self.y > 0:
                self.y -= 1
        elif ch == "c":
            self.reset()

    def _put(self, ch):
        if unicodedata.combining(ch) or ch in "​‌‍︎️":
            return
        w = 2 if unicodedata.east_asian_width(ch) in "WF" else 1
        if self.x + w > self.cols:
            self.x = 0
            self._lf()
        row = self.buf[self.y]
        row[self.x] = ch
        if self.x + 1 < self.cols:
            if w == 2:
                row[self.x + 1] = ""
            elif row[self.x + 1] == "":
                row[self.x + 1] = " "
        self.x += w

    def _lf(self):
        if self.y == self.bot:
            self._scroll(1)
        elif self.y < self.rows - 1:
            self.y += 1

    def _blank(self):
        return [" "] * self.cols

    def _scroll(self, n):  # n > 0: content moves up
        for _ in range(abs(n)):
            if n > 0:
                del self.buf[self.top]
                self.buf.insert(self.bot, self._blank())
            else:
                del self.buf[self.bot]
                self.buf.insert(self.top, self._blank())

    def _csi(self, params, final):
        priv = ""
        if params and params[0] in "?<=>":
            priv, params = params[0], params[1:]
        ps = []
        for p in params.split(";") if params else []:
            p = p.split(":")[0]
            ps.append(int(p) if p.isdigit() else 0)

        def arg(i, d=1):
            v = ps[i] if i < len(ps) else 0
            return v if v else d

        if priv:
            if final in "hl" and any(v in (47, 1047, 1049) for v in ps):
                self.buf = [self._blank() for _ in range(self.rows)]
            return
        cx = min(self.x, self.cols - 1)
        clampy = lambda v: max(0, min(self.rows - 1, v))
        clampx = lambda v: max(0, min(self.cols - 1, v))
        if final == "A":
            self.y = clampy(self.y - arg(0))
        elif final in "Be":
            self.y = clampy(self.y + arg(0))
        elif final in "Ca":
            self.x = clampx(cx + arg(0))
        elif final == "D":
            self.x = clampx(cx - arg(0))
        elif final == "E":
            self.x, self.y = 0, clampy(self.y + arg(0))
        elif final == "F":
            self.x, self.y = 0, clampy(self.y - arg(0))
        elif final in "G`":
            self.x = clampx(arg(0) - 1)
        elif final in "Hf":
            self.y, self.x = clampy(arg(0) - 1), clampx(arg(1) - 1)
        elif final == "d":
            self.y = clampy(arg(0) - 1)
        elif final == "J":
            mode = ps[0] if ps else 0
            if mode == 0:
                self.buf[self.y][cx:] = [" "] * (self.cols - cx)
                for r in range(self.y + 1, self.rows):
                    self.buf[r] = self._blank()
            elif mode == 1:
                self.buf[self.y][: cx + 1] = [" "] * (cx + 1)
                for r in range(self.y):
                    self.buf[r] = self._blank()
            else:
                self.buf = [self._blank() for _ in range(self.rows)]
        elif final == "K":
            mode = ps[0] if ps else 0
            row = self.buf[self.y]
            if mode == 0:
                row[cx:] = [" "] * (self.cols - cx)
            elif mode == 1:
                row[: cx + 1] = [" "] * (cx + 1)
            else:
                self.buf[self.y] = self._blank()
        elif final == "L":
            if self.top <= self.y <= self.bot:
                for _ in range(arg(0)):
                    del self.buf[self.bot]
                    self.buf.insert(self.y, self._blank())
        elif final == "M":
            if self.top <= self.y <= self.bot:
                for _ in range(arg(0)):
                    del self.buf[self.y]
                    self.buf.insert(self.bot, self._blank())
        elif final == "P":
            n = arg(0)
            row = self.buf[self.y]
            self.buf[self.y] = (row[:cx] + row[cx + n:] + [" "] * n)[: self.cols]
        elif final == "@":
            n = arg(0)
            row = self.buf[self.y]
            self.buf[self.y] = (row[:cx] + [" "] * n + row[cx:])[: self.cols]
        elif final == "X":
            n = arg(0)
            row = self.buf[self.y]
            row[cx: cx + n] = [" "] * len(row[cx: cx + n])
        elif final == "S":
            self._scroll(arg(0))
        elif final == "T":
            self._scroll(-arg(0))
        elif final == "r":
            self.top, self.bot = clampy(arg(0) - 1), clampy(arg(1, self.rows) - 1)
            self.x = self.y = 0
        elif final == "s":
            self.saved = (self.x, self.y)
        elif final == "u":
            self.x, self.y = self.saved


# ---------------------------------------------------------------------------
# Harness: processes, pseudo-terminals, the watch log, checks.


def clean_env():
    """The environment for claude: without the parent Claude Code session."""
    env = {k: v for k, v in os.environ.items()
           if not (k.startswith(("CLAUDE", "MCP", "ORCA_", "TERM_PROGRAM")) or k == "AI_AGENT")}
    env["TERM"] = "xterm-256color"
    # Keep the claude under test fixed for the whole run.
    env["DISABLE_AUTOUPDATER"] = "1"
    return env


def descendants(pid):
    try:
        out = subprocess.run(["ps", "-A", "-o", "pid=,ppid="], capture_output=True, text=True).stdout
    except OSError:
        return []
    kids = {}
    for line in out.splitlines():
        p = line.split()
        if len(p) == 2:
            kids.setdefault(int(p[1]), []).append(int(p[0]))
    found, todo = [], [pid]
    while todo:
        for c in kids.get(todo.pop(), []):
            found.append(c)
            todo.append(c)
    return found


class Agent:
    """One interactive claude in a pseudo-terminal. All reading happens on the
    harness thread (select), so the master is never closed under a blocked
    read (that can wedge the process on macOS)."""

    def __init__(self, h, name, extra_args=()):
        self.h, self.name = h, name
        self.cwd = os.path.join(WORK, name)
        self.screen = Screen(COLS, ROWS)
        self.raw = open(os.path.join(E, f"{name}.raw"), "wb")
        self.master, slave = os.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))
        self.tty = os.path.basename(os.ttyname(slave))
        cmd = [CLAUDE, "--model", MODEL, "--setting-sources", "project",
               "--settings", os.path.join(E, "claude", "settings.json"),
               "--strict-mcp-config", "--mcp-config", os.path.join(E, "mcp.json"), *extra_args]
        env = clean_env()
        env.update(FARERO_HOME=E, FARERO_SOCKET=h.env["FARERO_SOCKET"])
        self.proc = subprocess.Popen(
            cmd, stdin=slave, stdout=slave, stderr=slave, cwd=self.cwd, env=env,
            start_new_session=True, preexec_fn=lambda: fcntl.ioctl(0, termios.TIOCSCTTY, 0))
        os.close(slave)
        os.set_blocking(self.master, False)
        self.eof = False
        self.exited_at = None
        self.sid = None  # farero session id, once seen

    def on_readable(self):
        try:
            b = os.read(self.master, 65536)
        except BlockingIOError:
            return
        except OSError:
            b = b""
        if not b:
            self.eof = True
            return
        self.raw.write(b)
        self.screen.feed(b)

    def poll_exit(self):
        if self.exited_at is None and self.proc.poll() is not None:
            self.exited_at = time.time()

    def send(self, b):
        os.write(self.master, b)

    def type_line(self, text):
        # Enter goes separately so the TUI does not take the line as a paste.
        self.send(text.encode())
        self.h.sleep(0.6)
        self.send(ENTER)

    def text(self):
        return self.screen.text()

    def save_screen(self, label):
        with open(os.path.join(E, f"{self.name}.screens.txt"), "a") as f:
            f.write(f"===== {label} @ {time.strftime('%H:%M:%S')} =====\n{self.text()}\n")

    def close(self):
        if self.proc.poll() is None:
            kids = descendants(self.proc.pid)
            for sig, wait in ((signal.SIGTERM, 3), (signal.SIGKILL, 2)):
                try:
                    os.killpg(self.proc.pid, sig)
                except ProcessLookupError:
                    pass
                try:
                    self.proc.wait(wait)
                    break
                except subprocess.TimeoutExpired:
                    pass
            for k in kids:
                try:
                    os.kill(k, signal.SIGKILL)
                except ProcessLookupError:
                    pass
        # The child is gone, so nothing reads the master any more.
        try:
            os.close(self.master)
        except OSError:
            pass
        self.raw.close()


class Harness:
    def __init__(self):
        self.failed = False
        self.step_failed = False
        self.agents = []
        self.daemon = self.watch_proc = None
        self.env = dict(os.environ, FARERO_HOME=E, FARERO_SOCKET=os.path.join(E, "d.sock"),
                        FARERO_CLAUDE_CONFIG_DIR=os.path.join(E, "claude"))
        self.watch_path = os.path.join(E, "watch.log")
        self.watch_pos = 0
        self.watch_partial = b""
        self.events = []  # parsed watch lines: {"t","type","data","line"}

    # -- reporting

    def section(self, title):
        print(f"==> {title}", flush=True)
        self.step_failed = False

    def check(self, cond, msg, detail=None):
        if cond:
            print(f"  ok   {msg}", flush=True)
        else:
            print(f"  FAIL {msg}", flush=True)
            if detail:
                print(f"       {detail}", flush=True)
            if not self.step_failed:
                self.diag()
            self.failed = self.step_failed = True
        return bool(cond)

    def note(self, msg):
        print(f"       {msg}", flush=True)

    def diag(self, n=25):
        print("  ---- diagnostics", flush=True)
        self.poll_watch()
        print(f"  last {n} watch lines:")
        for e in self.events[-n:]:
            print("    " + e["line"][:300])
        for a in self.agents:
            if a.sid:
                print(f"  hook events of {a.name} ({a.sid}):")
                for ts, ev, tool in self.hook_events(a.sid)[-15:]:
                    print(f"    {time.strftime('%H:%M:%S', time.localtime(ts))}.{int(ts * 1000) % 1000:03d} {ev} {tool}")
            print(f"  screen of {a.name}:")
            for line in a.text().splitlines():
                if line.strip():
                    print("    | " + line)
        try:
            with open(os.path.join(E, "farerod.log")) as f:
                tail = f.read().splitlines()[-12:]
            print("  farerod.log tail:")
            for line in tail:
                print("    " + line[:300])
        except OSError:
            pass
        print("  ----", flush=True)

    # -- event loop

    def pump(self, timeout=0.1):
        fds = {a.master: a for a in self.agents if not a.eof}
        if fds:
            r, _, _ = select.select(list(fds), [], [], timeout)
            for fd in r:
                fds[fd].on_readable()
        else:
            time.sleep(timeout)
        for a in self.agents:
            a.poll_exit()
        self.poll_watch()

    def sleep(self, secs):
        end = time.time() + secs
        while time.time() < end:
            self.pump(min(0.1, max(0.0, end - time.time())))

    def wait(self, pred, timeout):
        end = time.time() + timeout
        while True:
            v = pred()
            if v:
                return v
            if time.time() >= end:
                return None
            self.pump(0.1)

    # -- farerod / devctl

    def devctl(self, *args, timeout=60):
        p = subprocess.run([os.path.join(BIN, "farero-devctl"), *args], env=self.env,
                           capture_output=True, text=True, timeout=timeout)
        if p.returncode != 0:
            raise Abort(f"farero-devctl {' '.join(args)} failed: {p.stderr.strip()}")
        first, _, rest = p.stdout.partition("\n")
        return first.strip(), json.loads(rest) if rest.strip() else None

    def log_rows(self, n=100):
        _, rows = self.devctl("log", "-n", str(n))
        return rows or []

    def hook_events(self, sid):
        try:
            db = sqlite3.connect(f"file:{os.path.join(E, 'farero.db')}?mode=ro", uri=True, timeout=5)
            try:
                rows = db.execute("SELECT ts, event, tool_name FROM hook_events WHERE session_id = ? ORDER BY id",
                                  (sid,)).fetchall()
            finally:
                db.close()
        except sqlite3.Error:
            return []
        return [(ts / 1000.0, ev, tool) for ts, ev, tool in rows]

    def poll_watch(self):
        try:
            with open(self.watch_path, "rb") as f:
                f.seek(self.watch_pos)
                data = f.read()
        except OSError:
            return
        self.watch_pos += len(data)
        data = self.watch_partial + data
        *lines, self.watch_partial = data.split(b"\n")
        for raw in lines:
            line = raw.decode("utf-8", "replace")
            m = re.match(r"(\d\d):(\d\d):(\d\d)\.(\d{3}) (\S+) (.*)$", line)
            if not m:
                self.events.append({"t": time.time(), "type": "?", "data": None, "line": line})
                continue
            lt = time.localtime()
            t = time.mktime((lt.tm_year, lt.tm_mon, lt.tm_mday, int(m[1]), int(m[2]), int(m[3]), 0, 0, -1)) + int(m[4]) / 1000
            if t > time.time() + 3600:  # written before midnight
                t -= 86400
            try:
                d = json.loads(m[6])
            except ValueError:
                d = None
            self.events.append({"t": t, "type": m[5], "data": d, "line": line})

    def find_event(self, typ, pred=lambda d: True, since=0.0):
        for e in self.events:
            if e["type"] == typ and e["t"] >= since and e["data"] is not None and pred(e["data"]):
                return e
        return None

    def wait_event(self, typ, pred=lambda d: True, since=0.0, timeout=30):
        return self.wait(lambda: self.find_event(typ, pred, since), timeout)

    def history(self, sid, since=0.0):
        """[(t, status, current_tool)] of a session, from session.updated."""
        out = []
        for e in self.events:
            if e["t"] < since or e["data"] is None:
                continue
            if e["type"] == "session.updated" and e["data"].get("id") == sid:
                out.append((e["t"], e["data"].get("status"), e["data"].get("current_tool", "")))
            elif e["type"] == "state.snapshot":
                for s in e["data"].get("sessions") or []:
                    if s.get("id") == sid:
                        out.append((e["t"], s.get("status"), s.get("current_tool", "")))
        return out

    def session(self, sid):
        last = None
        for e in self.events:
            if e["type"] == "session.updated" and e["data"] and e["data"].get("id") == sid:
                last = e["data"]
        return last

    def status(self, sid):
        s = self.session(sid)
        return s and s.get("status")

    def wait_status(self, sid, status, since, timeout, tool=None):
        def hit():
            for t, st, cur in self.history(sid, since):
                if st == status and (tool is None or cur == tool):
                    return t
            return None
        return self.wait(hit, timeout)

    # -- lifecycle

    def start_daemon(self):
        log = open(os.path.join(E, "farerod.log"), "w")
        self.daemon = subprocess.Popen([os.path.join(BIN, "farerod"), "--dev", "--debug"],
                                       env=self.env, stdout=log, stderr=subprocess.STDOUT)

        def listening():
            with open(os.path.join(E, "farerod.log")) as f:
                return "gateway listening" in f.read()
        if not self.wait(listening, 15):
            raise Abort("farerod did not start its gateway (see farerod.log)")

    def start_watch(self):
        out = open(self.watch_path, "wb")
        self.watch_proc = subprocess.Popen([os.path.join(BIN, "farero-devctl"), "watch"], env=self.env,
                                           stdout=out, stderr=subprocess.STDOUT)
        if not self.wait_event("state.snapshot", timeout=10):
            raise Abort("farero-devctl watch got no snapshot")

    def cleanup(self):
        for a in self.agents:
            try:
                a.close()
            except Exception as ex:  # keep cleaning up
                print(f"  note: closing {a.name}: {ex}")
        for p in (self.watch_proc, self.daemon):
            if p and p.poll() is None:
                p.terminate()
                try:
                    p.wait(5)
                except subprocess.TimeoutExpired:
                    p.kill()


# ---------------------------------------------------------------------------
# Steps


def ready(a):
    """The input box is idle (footer shows the shortcuts hint)."""
    return "? for shortcuts" in a.text()


def dialog_open(a):
    return "Do you want to proceed?" in a.text()


def selected_option(a):
    m = re.search(r"❯\s*(\d)\.\s*(\S+)", a.text().split("Do you want to proceed?")[-1])
    return m and (m[1], m[2])


def step_register(h):
    h.section("register farero with an isolated Claude config (agentcfg apply)")
    cfg = os.path.join(E, "claude")
    # A user settings.json with its own formatting and key order: remove must
    # give these exact bytes back.
    user = {"model": "haiku", "permissions": {"allow": ["Bash(npm test:*)"]},
            "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "true"}]}]}, "env": {}}
    original = (json.dumps(user, indent=4) + "\n").encode()
    with open(os.path.join(cfg, "settings.json"), "wb") as f:
        f.write(original)
    with open(os.path.join(E, "settings.orig.json"), "wb") as f:
        f.write(original)

    typ, st = h.devctl("agentcfg", "apply")
    with open(os.path.join(E, "apply.json"), "w") as f:
        json.dump(st, f, indent=2)
    if not h.check(typ == "agentcfg.status" and st and st.get("hooks_installed") and st.get("allow_installed")
                   and st.get("mcp_installed"), "apply reports hooks, allow rule and MCP server installed",
                   json.dumps(st, ensure_ascii=False)[:400]):
        raise Abort("registration failed")

    s = json.load(open(os.path.join(cfg, "settings.json")))
    hooks = s.get("hooks", {})
    farero = {}
    for ev, groups in hooks.items():
        for g in groups:
            for hk in g.get("hooks", []):
                if "farero-hook" in hk.get("command", ""):
                    farero.setdefault(ev, []).append(hk)
    h.check(sorted(farero) == sorted(HOOK_EVENTS),
            f"farero hooks on exactly the {len(HOOK_EVENTS)} events, StopFailure included",
            f"got {sorted(farero)}")
    h.check(all(len(v) == 1 and v[0].get("timeout") == 660 and "--agent claude" in v[0]["command"]
                for v in farero.values()), "each farero hook: one entry, `--agent claude`, timeout 660")
    h.check(any(hk.get("command") == "true" for g in hooks.get("Stop", []) for hk in g.get("hooks", [])),
            "the user's own Stop hook is kept")
    allow = s.get("permissions", {}).get("allow", [])
    h.check("Bash(npm test:*)" in allow and "mcp__farero__*" in allow, "allow rule added next to the user's rule",
            f"allow={allow}")
    h.check(s.get("model") == "haiku" and s.get("env") == {}, "other user settings are untouched")

    srv = json.load(open(os.path.join(cfg, ".claude.json"))).get("mcpServers", {}).get("farero")
    h.check(srv and srv.get("timeout") == 660000 and "--headers" in srv.get("headersHelper", ""),
            "MCP server entry has headersHelper and timeout 660000", json.dumps(srv)[:300])
    if not srv:
        raise Abort("no MCP server entry")
    with open(os.path.join(E, "mcp.json"), "w") as f:
        json.dump({"mcpServers": {"farero": srv}}, f)


def step_start(h):
    h.section("two claude sessions at the same time (app connected: farero-devctl watch)")
    h.start_watch()
    since = time.time()
    alpha = Agent(h, "alpha", ["--allowedTools", "Bash(ping:*)"])
    h.agents.append(alpha)
    beta = Agent(h, "beta")
    h.agents.append(beta)

    def seen(a):
        def f():
            for e in h.events:
                d = e["data"]
                if e["t"] >= since and e["type"] == "session.updated" and d and d.get("pid") == a.proc.pid \
                        and d.get("status") == "waiting_input":
                    return d
            return None
        return f

    for a in (alpha, beta):
        d = h.wait(seen(a), 60)
        if d:
            a.sid = d["id"]
        ok = h.wait(lambda: ready(a), 30)
        a.save_screen("started")
        if not h.check(d and ok, f"{a.name}: SessionStart reached farerod (waiting_input) and the prompt is ready"):
            raise Abort(f"{a.name} did not start")
    h.sleep(1.5)

    sa, sb = h.session(alpha.sid), h.session(beta.sid)
    h.check(alpha.sid != beta.sid, "two distinct sessions", f"{alpha.sid} / {beta.sid}")
    for a, s in ((alpha, sa), (beta, sb)):
        h.check(os.path.basename(s.get("cwd", "")) == a.name and s.get("agent") == "claude"
                and s.get("started_at"), f"{a.name}: cwd folder {a.name!r}, agent claude, started_at set",
                json.dumps(s, ensure_ascii=False))
        h.check(s.get("pid") == a.proc.pid and s.get("tty") == a.tty,
                f"{a.name}: pid and tty are the claude process's ({a.proc.pid}, {a.tty})",
                f"session pid={s.get('pid')} tty={s.get('tty')!r}")
    return alpha, beta


def step_alternate(h, alpha, beta):
    h.section("status alternates running -> waiting_input")
    t0 = time.time()
    for prompt, word in (("Reply with just the word hi.", "hi"), ("Reply with just the word ok.", "ok")):
        since = time.time()
        for a in (alpha, beta):  # both sessions work at the same time
            a.type_line(prompt)
        for a in (alpha, beta):
            tr = h.wait_status(a.sid, "running", since, 20)
            tw = tr and h.wait_status(a.sid, "waiting_input", tr, 60)
            h.wait(lambda: ready(a), 10)
            a.save_screen(f"after {word}")
            h.check(tr and tw, f"{a.name}: {prompt!r} -> running -> waiting_input"
                    + (f" ({tw - since:.1f} s)" if tw else ""))
            # The model may answer in another language (the user's CLAUDE.md
            # still loads), so only look for a reply after the prompt.
            h.check(re.search(rf"(?s)❯ {re.escape(prompt)}\n+⏺ \S", a.text()), f"{a.name}: the reply is on the terminal")
    for a in (alpha, beta):
        seq = []
        for _, st, _ in h.history(a.sid, t0):
            if not seq or seq[-1] != st:
                seq.append(st)
        h.check(seq[:4] == ["running", "waiting_input", "running", "waiting_input"],
                f"{a.name}: status sequence running, waiting_input, running, waiting_input", f"got {seq}")


def step_esc(h, alpha):
    h.section("(a) Esc during a running tool -> waiting_input (no hook event)")
    since = time.time()
    alpha.type_line("Run this exact command with the Bash tool: ping -c 30 127.0.0.1")
    t = h.wait_status(alpha.sid, "running", since, 60, tool="Bash")
    if not h.check(t, "alpha: running with current_tool Bash"):
        return None
    h.sleep(2.5)  # let ping run
    alpha.save_screen("ping running")
    h.check(h.status(alpha.sid) == "running", "alpha: still running before Esc")
    t_esc = time.time()
    alpha.send(ESC)
    tw = h.wait_status(alpha.sid, "waiting_input", t_esc, 15)
    alpha.save_screen("after Esc")
    lat = tw - t_esc if tw else None
    h.check(tw and lat <= 8, "alpha: waiting_input within 8 s of Esc"
            + (f" ({lat:.1f} s)" if tw else " (not within 15 s)"))
    h.check(h.session(alpha.sid).get("current_tool") == "", "alpha: current_tool cleared",
            f"current_tool={h.session(alpha.sid).get('current_tool')!r}")
    evs = [ev for ts, ev, _ in h.hook_events(alpha.sid) if ts >= t_esc]
    h.note(f"hook events after Esc until then: {evs or 'none'}")
    h.check("Interrupted" in alpha.text(), "alpha: the terminal shows the interruption")
    return lat


def wait_card(h, a, since, command):
    ev = h.wait_event("approval.request", lambda d: d.get("session_id") == a.sid and d.get("tool") == "Bash"
                      and command in json.dumps(d.get("input")), since, 60)
    if not h.check(ev, f"{a.name}: approval.request for Bash {command!r}"):
        return None, None
    ok = h.wait(lambda: dialog_open(a), 15)
    a.save_screen(f"dialog {command}")
    h.check(ok, f"{a.name}: the terminal shows its own permission prompt at the same time")
    return ev["data"], ev["t"]


def find_row(h, command, since):
    for r in h.log_rows():
        if r.get("kind") == "agent" and r.get("tool") == "Bash" and command in json.dumps(r.get("input")):
            return r
    return None


def step_deny(h, beta):
    h.section("(b) No at the terminal prompt while the app is connected (+ (e) permission_prompt)")
    path = os.path.join(beta.cwd, "e2e-deny.txt")
    since = time.time()
    beta.type_line("Run this exact shell command with the Bash tool: touch e2e-deny.txt")
    card, t_req = wait_card(h, beta, since, "touch e2e-deny.txt")
    if not card:
        return
    h.wait(lambda: h.status(beta.sid) == "waiting_approval", 3)
    h.check(h.status(beta.sid) == "waiting_approval", "beta: waiting_approval")
    # Claude Code sends Notification(permission_prompt) about 6 s after an
    # unanswered PermissionRequest.
    note = h.wait(lambda: [ts for ts, ev, _ in h.hook_events(beta.sid) if ts >= t_req - 0.5 and ev == "Notification"],
                  max(0.0, t_req + 12 - time.time()))
    h.sleep(max(0.0, t_req + 8.5 - time.time()))
    h.check(note, "beta: a Notification hook event arrived while the prompt waited"
            + (f" ({note[0] - t_req:.1f} s after the card)" if note else ""))
    statuses = {st for _, st, _ in h.history(beta.sid, t_req)}
    h.check(h.status(beta.sid) == "waiting_approval" and statuses <= {"waiting_approval"},
            "beta: still waiting_approval after the permission_prompt Notification",
            f"statuses since card: {statuses}")
    beta.send(DOWN)
    h.sleep(0.4)
    beta.send(DOWN)
    h.sleep(0.6)
    sel = selected_option(beta)
    beta.save_screen("No selected")
    if not h.check(sel and sel[1].startswith("No"), "beta: 'No' selected in the terminal prompt", f"selected {sel}"):
        return
    t_no = time.time()
    beta.send(ENTER)
    c = h.wait_event("approval.cancelled", lambda d: d.get("approval_id") == card["id"], t_no - 0.5, 10)
    h.check(c, "beta: approval.cancelled for the card" + (f" ({c['t'] - t_no:.1f} s, reason {c['data'].get('reason')!r})" if c else ""))
    tw = h.wait_status(beta.sid, "waiting_input", t_no, 10)
    h.check(tw, "beta: waiting_input" + (f" ({tw - t_no:.1f} s after No)" if tw else ""))
    h.sleep(3.5)  # one interrupt sweep
    cur = h.session(beta.sid).get("current_tool")
    h.check(cur == "", "beta: current_tool cleared after No", f"status {h.status(beta.sid)!r}, current_tool={cur!r}")
    beta.save_screen("after No")
    row = h.wait(lambda: find_row(h, "e2e-deny.txt", since), 5)
    h.check(row and row.get("decision") == "cancelled" and row.get("reason") == "",
            "audit log: decision cancelled, empty reason",
            row and f"decision={row.get('decision')!r} reason={row.get('reason')!r}")
    h.check(not os.path.exists(path), "the denied command did not run")


def step_allow(h, beta):
    h.section("(c) Yes at the terminal prompt while the app is connected")
    path = os.path.join(beta.cwd, "e2e-allow.txt")
    h.wait(lambda: ready(beta), 10)
    since = time.time()
    beta.type_line("Run this exact shell command with the Bash tool: touch e2e-allow.txt")
    card, t_req = wait_card(h, beta, since, "touch e2e-allow.txt")
    if not card:
        return
    h.sleep(1.0)
    sel = selected_option(beta)
    if not h.check(sel and sel[1].startswith("Yes"), "beta: 'Yes' selected in the terminal prompt", f"selected {sel}"):
        return
    t_yes = time.time()
    beta.send(ENTER)
    c = h.wait_event("approval.cancelled", lambda d: d.get("approval_id") == card["id"], t_yes - 0.5, 15)
    h.check(c, "beta: approval.cancelled for the card" + (f" ({c['t'] - t_yes:.1f} s, reason {c['data'].get('reason')!r})" if c else ""))
    h.check(h.wait(lambda: os.path.exists(path), 10), "the allowed command ran (e2e-allow.txt exists)")
    row = h.wait(lambda: find_row(h, "e2e-allow.txt", since), 5)
    h.check(row and row.get("decision") == "cancelled" and row.get("reason") == "answered_in_agent",
            "audit log: decision cancelled, reason answered_in_agent",
            row and f"decision={row.get('decision')!r} reason={row.get('reason')!r}")
    tw = h.wait_status(beta.sid, "waiting_input", t_yes, 60)
    seq = [st for _, st, _ in h.history(beta.sid, t_yes)]
    h.check(tw and "running" in seq[: seq.index("waiting_input")] if tw else False,
            "beta: running after Yes, then waiting_input after Stop", f"statuses after Yes: {seq}")
    h.wait(lambda: ready(beta), 10)
    beta.save_screen("after Yes")
    h.check(h.status(beta.sid) == "waiting_input", "beta: ends at waiting_input")


def step_exit_pending(h, alpha):
    """/exit while a PermissionRequest hook still waits.

    The terminal prompt takes every key while it is open, so the user gets
    the input box back only after answering it. "Yes" does not end the hook:
    farerod keeps the card until the tool's PostToolUse, so /exit typed while
    the approved tool still runs exits with the hook waiting. (Esc there
    ends the hook as well, so it is not used.)"""
    h.section("(d) /exit while a PermissionRequest hook waits")
    h.wait(lambda: ready(alpha), 10)
    since = time.time()
    alpha.type_line("Run this exact shell command with the Bash tool: python3 -c 'import time; time.sleep(30)'")
    card, t_req = wait_card(h, alpha, since, "time.sleep(30)")
    if not card:
        return None
    h.sleep(1.0)
    alpha.send(ENTER)  # Yes: the tool runs; the hook waits for PostToolUse
    h.sleep(2.5)
    alpha.save_screen("approved tool running")
    open_card = not h.find_event("approval.cancelled", lambda d: d.get("approval_id") == card["id"])
    if not h.check(open_card, "alpha: the hook still waits while the approved tool runs (card open)"):
        return None
    t_exit = time.time()
    alpha.type_line("/exit")
    # Claude Code may first ask what to do with the running shell
    # ("Background work is running"); option 1 exits and stops it.
    if h.wait(lambda: alpha.exited_at or "Background work is running" in alpha.text(), 10) is True:
        alpha.save_screen("exit confirmation")
        h.note("claude asked to confirm the exit (background work); answered 'Exit and stop tasks'")
        h.sleep(0.5)
        alpha.send(ENTER)
    gone = h.wait(lambda: alpha.exited_at, 20)
    if not h.check(gone, "alpha: claude exited after /exit"):
        return None
    te = alpha.exited_at
    h.note(f"claude exited {te - t_exit:.1f} s after /exit")
    if h.find_event("approval.cancelled", lambda d: d.get("approval_id") == card["id"], 0) and \
            [1 for ts, ev, _ in h.hook_events(alpha.sid) if ev == "PostToolUse" and ts >= t_exit]:
        h.note("the tool finished (PostToolUse) before the exit, so no hook was waiting any more")
    c = h.wait_event("approval.cancelled", lambda d: d.get("approval_id") == card["id"], 0, 5)
    h.check(c, "alpha: the card is withdrawn" + (f" (reason {c['data'].get('reason')!r})" if c else ""))
    final = h.wait(lambda: h.status(alpha.sid) in ("unknown", "ended") and h.status(alpha.sid), 15)
    t_final = h.history(alpha.sid, te - 1)[-1][0] if final else None
    ends = [ts for ts, ev, _ in h.hook_events(alpha.sid) if ts >= t_exit and ev == "SessionEnd"]
    h.note(f"SessionEnd {'sent' if ends else 'not sent'}; status {final!r}"
           + (f" {t_final - te:+.1f} s from the process exit" if final else ""))
    h.check(final and t_final - te <= 10 and (final == "ended") == bool(ends),
            f"alpha: {final or 'ended/unknown'} within 10 s of the process exit"
            + (" (SessionEnd)" if ends else " (no SessionEnd)"))
    row = h.wait(lambda: find_row(h, "time.sleep(30)", since), 5)
    h.note(f"audit log row: {row and (row.get('decision'), row.get('reason'))}")
    return (final, t_final - te) if final else None


def step_exit_killed(h):
    """A session whose agent dies without SessionEnd (kill -9) -> unknown."""
    h.section("(d) agent process gone without SessionEnd -> unknown")
    since = time.time()
    gamma = Agent(h, "gamma")
    h.agents.append(gamma)
    ev = h.wait(lambda: h.find_event("session.updated", lambda d: d.get("pid") == gamma.proc.pid, since), 60)
    if not h.check(ev and h.wait(lambda: ready(gamma), 30), "gamma: started"):
        return None
    gamma.sid = ev["data"]["id"]
    h.sleep(1.0)
    kids = descendants(gamma.proc.pid)
    os.kill(gamma.proc.pid, signal.SIGKILL)
    for k in kids:
        try:
            os.kill(k, signal.SIGKILL)
        except ProcessLookupError:
            pass
    h.wait(lambda: gamma.exited_at, 5)
    te = gamma.exited_at or time.time()
    tu = h.wait_status(gamma.sid, "unknown", te - 0.5, 15)
    ends = [ts for ts, ev, _ in h.hook_events(gamma.sid) if ev == "SessionEnd"]
    h.check(tu and not ends and tu - te <= 10, "gamma: unknown within 10 s of the process exit (no SessionEnd)"
            + (f" ({tu - te:.1f} s)" if tu else f"; status {h.status(gamma.sid)!r}"))
    return tu - te if tu else None


def step_exit_plain(h, beta):
    h.section("plain /exit -> ended")
    h.wait(lambda: ready(beta), 10)
    t_exit = time.time()
    beta.type_line("/exit")
    gone = h.wait(lambda: beta.exited_at, 20)
    if not h.check(gone, "beta: claude exited after /exit"):
        return None
    tf = h.wait_status(beta.sid, "ended", t_exit, 10)
    h.check(tf, "beta: ended (SessionEnd)" + (f" ({tf - beta.exited_at:+.1f} s from the process exit)" if tf else ""),
            f"status {h.status(beta.sid)!r}")
    h.sleep(8)  # the exit sweep must not turn ended into unknown
    h.check(h.status(beta.sid) == "ended", "beta: stays ended")
    for a in h.agents:
        if a.sid and a.exited_at:
            early = [t for t, st, _ in h.history(a.sid) if st == "unknown" and t < a.exited_at]
            h.check(not early, f"{a.name}: never unknown while its claude process was running")
    return tf


def step_remove(h):
    h.section("remove farero from the isolated Claude config (agentcfg remove)")
    typ, st = h.devctl("agentcfg", "remove")
    h.check(st and not (st.get("hooks_installed") or st.get("allow_installed") or st.get("mcp_installed")),
            "remove reports nothing installed", json.dumps(st, ensure_ascii=False)[:300])
    now = open(os.path.join(E, "claude", "settings.json"), "rb").read()
    orig = open(os.path.join(E, "settings.orig.json"), "rb").read()
    h.check(now == orig, "settings.json is byte-identical to the file before apply",
            f"{len(now)} vs {len(orig)} bytes; diff {E}/claude/settings.json {E}/settings.orig.json")
    cj = json.load(open(os.path.join(E, "claude", ".claude.json")))
    h.check("farero" not in (cj.get("mcpServers") or {}), "the MCP server entry is gone from .claude.json")


def main():
    shutil.rmtree(E, ignore_errors=True)
    os.makedirs(os.path.join(E, "claude"))
    for n in ("alpha", "beta", "gamma"):
        d = os.path.join(WORK, n)
        shutil.rmtree(d, ignore_errors=True)
        os.makedirs(d)
    if os.environ.get("E2E_NO_BUILD") != "1":
        print("==> build dev binaries", flush=True)
        if subprocess.run(["go", "build", "-p", "2", "-tags", "farero_dev", "-o", BIN + "/", "./cmd/..."],
                          cwd=os.path.join(ROOT, "daemon")).returncode != 0:
            return 1

    h = Harness()
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(130))
    timings = {}
    try:
        h.start_daemon()
        step_register(h)
        alpha, beta = step_start(h)
        step_alternate(h, alpha, beta)
        timings["a"] = step_esc(h, alpha)
        step_deny(h, beta)
        step_allow(h, beta)
        timings["d"] = step_exit_pending(h, alpha)
        step_exit_plain(h, beta)
        timings["kill"] = step_exit_killed(h)
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
        h.cleanup()

    warn = []
    try:
        warn = [l for l in open(os.path.join(E, "farerod.log")) if re.search(r"level=(ERROR|WARN)", l)]
    except OSError:
        pass
    if warn:
        print("  note: farerod logged warnings/errors:")
        for l in warn[-10:]:
            print("    " + l.rstrip()[:300])
    if timings.get("a") is not None:
        print(f"  timing: Esc -> waiting_input {timings['a']:.1f} s")
    if timings.get("d"):
        print(f"  timing: /exit with a waiting hook: process exit -> {timings['d'][0]} {timings['d'][1]:+.1f} s")
    if timings.get("kill") is not None:
        print(f"  timing: kill -9 -> unknown {timings['kill']:.1f} s")
    print(f"==> E2E SESSIONS {'FAILED' if h.failed else 'PASSED'} (logs in {E})")
    return 1 if h.failed else 0


if __name__ == "__main__":
    sys.exit(main())
