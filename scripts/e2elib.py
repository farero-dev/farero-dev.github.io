"""Shared harness for the end-to-end checks with a real, interactive Claude Code
(scripts/e2e-sessions.py for M2, scripts/e2e-approvals.py for M3).

Each `claude` runs in a pseudo-terminal owned by the script: headless, no
Terminal window, no focus stealing. The script types into it like a user and
checks what a development farerod reports through `farero-devctl watch` (a
stand-in for the app), its audit log and its database.

Isolation is the same as scripts/e2e.sh: FARERO_HOME/FARERO_SOCKET point at a
directory under /tmp, and farero is registered into a throwaway Claude config
dir (FARERO_CLAUDE_CONFIG_DIR=<dir>/claude) whose generated settings.json and
MCP entry are passed to `claude` with --settings / --mcp-config
--strict-mcp-config --setting-sources project. The developer's real ~/.claude
settings and MCP servers are never touched; the parent Claude Code session's
CLAUDE*/MCP* variables are removed from claude's environment.
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
import termios
import time
import unicodedata

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BIN = os.environ.get("E2E_BIN") or os.path.join(ROOT, "build", "dev")
MODEL = os.environ.get("E2E_MODEL", "haiku")
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
        self.cwd = os.path.join(h.work, name)
        self.screen = Screen(COLS, ROWS)
        self.raw = open(os.path.join(h.e, f"{name}.raw"), "wb")
        self.master, slave = os.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))
        self.tty = os.path.basename(os.ttyname(slave))
        cmd = [CLAUDE, "--model", MODEL, "--setting-sources", "project",
               "--settings", os.path.join(h.e, "claude", "settings.json"),
               "--strict-mcp-config", "--mcp-config", os.path.join(h.e, "mcp.json"), *extra_args]
        env = clean_env()
        env.update(FARERO_HOME=h.e, FARERO_SOCKET=h.env["FARERO_SOCKET"])
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
        with open(os.path.join(self.h.e, f"{self.name}.screens.txt"), "a") as f:
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
    """e is the run's directory (FARERO_HOME, logs); work holds the agents'
    folders."""

    def __init__(self, e, work, daemon_env=None):
        self.e, self.work = e, work
        self.daemon_env = daemon_env or {}
        self.failed = False
        self.step_failed = False
        self.agents = []
        self.daemon = self.watch_proc = None
        self.env = dict(os.environ, FARERO_HOME=e, FARERO_SOCKET=os.path.join(e, "d.sock"),
                        FARERO_CLAUDE_CONFIG_DIR=os.path.join(e, "claude"))
        self.watch_path = os.path.join(e, "watch.log")
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
            with open(os.path.join(self.e, "farerod.log")) as f:
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
            db = sqlite3.connect(f"file:{os.path.join(self.e, 'farero.db')}?mode=ro", uri=True, timeout=5)
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
        log = open(os.path.join(self.e, "farerod.log"), "w")
        self.daemon = subprocess.Popen([os.path.join(BIN, "farerod"), "--dev", "--debug"],
                                       env=dict(self.env, **self.daemon_env), stdout=log, stderr=subprocess.STDOUT)

        def listening():
            with open(os.path.join(self.e, "farerod.log")) as f:
                return "gateway listening" in f.read()
        if not self.wait(listening, 15):
            raise Abort("farerod did not start its gateway (see farerod.log)")

    def start_watch(self):
        """Connects the app stand-in (again: the log is appended to)."""
        since = time.time() - 1
        out = open(self.watch_path, "ab")
        self.watch_proc = subprocess.Popen([os.path.join(BIN, "farero-devctl"), "watch"], env=self.env,
                                           stdout=out, stderr=subprocess.STDOUT)
        if not self.wait_event("state.snapshot", since=since, timeout=10):
            raise Abort("farero-devctl watch got no snapshot")

    def stop_watch(self):
        """Disconnects the app stand-in: farerod has no UI client then."""
        if self.watch_proc and self.watch_proc.poll() is None:
            self.watch_proc.terminate()
            self.watch_proc.wait(5)
        self.watch_proc = None

    def answer(self, approval_id, answer):
        """Answers an approval card like the app (a second UI connection)."""
        typ, _ = self.devctl("answer", approval_id, answer)
        return typ

    def db_calls(self, since=0.0):
        """Audit log rows straight from the database, oldest first. Unlike
        `farero-devctl log` this does not connect as a UI client."""
        try:
            db = sqlite3.connect(f"file:{os.path.join(self.e, 'farero.db')}?mode=ro", uri=True, timeout=5)
            try:
                rows = db.execute("SELECT id, ts, kind, tool, input_json, decision, reason FROM calls "
                                  "WHERE ts >= ? ORDER BY id", (int(since * 1000),)).fetchall()
            finally:
                db.close()
        except sqlite3.Error:
            return []
        return [{"id": i, "t": ts / 1000.0, "kind": k, "tool": tool, "input": inp, "decision": d, "reason": r}
                for i, ts, k, tool, inp, d, r in rows]

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
# Shared steps and checks


def ready(a):
    """The input box is idle (footer shows the shortcuts hint, or the
    permission mode when one is on)."""
    t = a.text()
    return "? for shortcuts" in t or ("(shift+tab to cycle)" in t and "esc to interrupt" not in t)


def dialog_open(a):
    return "Do you want to proceed?" in a.text()


def selected_option(a):
    m = re.search(r"❯\s*(\d)\.\s*(\S+)", a.text().split("Do you want to proceed?")[-1])
    return m and (m[1], m[2])


def step_register(h):
    h.section("register farero with an isolated Claude config (agentcfg apply)")
    cfg = os.path.join(h.e, "claude")
    # A user settings.json with its own formatting and key order: remove must
    # give these exact bytes back.
    user = {"model": "haiku", "permissions": {"allow": ["Bash(npm test:*)"]},
            "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "true"}]}]}, "env": {}}
    original = (json.dumps(user, indent=4) + "\n").encode()
    with open(os.path.join(cfg, "settings.json"), "wb") as f:
        f.write(original)
    with open(os.path.join(h.e, "settings.orig.json"), "wb") as f:
        f.write(original)

    typ, st = h.devctl("agentcfg", "apply")
    with open(os.path.join(h.e, "apply.json"), "w") as f:
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
    with open(os.path.join(h.e, "mcp.json"), "w") as f:
        json.dump({"mcpServers": {"farero": srv}}, f)


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


def step_remove(h):
    h.section("remove farero from the isolated Claude config (agentcfg remove)")
    typ, st = h.devctl("agentcfg", "remove")
    h.check(st and not (st.get("hooks_installed") or st.get("allow_installed") or st.get("mcp_installed")),
            "remove reports nothing installed", json.dumps(st, ensure_ascii=False)[:300])
    now = open(os.path.join(h.e, "claude", "settings.json"), "rb").read()
    orig = open(os.path.join(h.e, "settings.orig.json"), "rb").read()
    h.check(now == orig, "settings.json is byte-identical to the file before apply",
            f"{len(now)} vs {len(orig)} bytes; diff {h.e}/claude/settings.json {h.e}/settings.orig.json")
    cj = json.load(open(os.path.join(h.e, "claude", ".claude.json")))
    h.check("farero" not in (cj.get("mcpServers") or {}), "the MCP server entry is gone from .claude.json")


def prepare(e, work, names):
    """A fresh run directory and agent folders, and the dev binaries unless
    E2E_NO_BUILD=1. Returns False when the build failed."""
    shutil.rmtree(e, ignore_errors=True)
    os.makedirs(os.path.join(e, "claude"))
    for n in names:
        d = os.path.join(work, n)
        shutil.rmtree(d, ignore_errors=True)
        os.makedirs(d)
    if os.environ.get("E2E_NO_BUILD") == "1":
        return True
    print("==> build dev binaries", flush=True)
    return subprocess.run(["go", "build", "-p", "2", "-tags", "farero_dev", "-o", BIN + "/", "./cmd/..."],
                          cwd=os.path.join(ROOT, "daemon")).returncode == 0


def print_daemon_warnings(e):
    warn = []
    try:
        warn = [l for l in open(os.path.join(e, "farerod.log")) if re.search(r"level=(ERROR|WARN)", l)]
    except OSError:
        pass
    if warn:
        print("  note: farerod logged warnings/errors:")
        for l in warn[-10:]:
            print("    " + l.rstrip()[:300])
