#!/usr/bin/env python3
"""End-to-end check of the notch UI (M6; 기능 명세서 F-01, 5-3, F-12) with the
real app bundle and a real, interactive Claude Code.

    scripts/e2e-notch.py

The app under test is build/Farero.app (scripts/build.sh; E2E_APP: another
bundle), started straight from its Contents/MacOS in development mode
(FARERO_SOCKET set: it registers no LaunchAgent and leaves the installed
/Applications/Farero.app and its farerod alone). It is the only UI that
answers cards: `farero-devctl watch` stays connected as an observer only.
farerod, the isolated Claude Code registration and the plugin credentials
are the ones of scripts/e2e-plugins.py (E2E_DIR, default /tmp/frm5:
everything in it is removed except dev-secrets.json and farero.db), so
scenario B redeploys the Railway service whoami of the project farero-e2e.

The script cannot press keys or click: posting input events needs the
Accessibility permission. With FARERO_DEBUG_CONTROL the app reads commands
from a named pipe instead: `hotkey allow|allow_session|deny` sends the
shortcut's Carbon hot-key event to the app (the event a key press produces,
handled by the same code), `pin` toggles the pinned island. E2E_MANUAL=1
asks the person at the keyboard to press ⌃⌥Y/⌃⌥S/⌃⌥N and to click 허용
instead, and checks which path the app took.

What it checks (MVP 구현 순서 M6 완료 기준):
  1. Hidden: with no live session the island is hidden, the panel has no
     window on screen, and the app is idle (top's idle wakeups, Q80).
  2. Compact: a new session shows the island around the notch (on-screen
     panel at the top of the screen, island as tall as the notch) with the
     greeting character; pinned, it expands to the session list.
  3. Scenario A in the notch: `npm install` -> the notch card (approval
     mode, the island grows below the menu bar, ⌃⌥Y/S/N registered) -> ⌃⌥Y
     -> the terminal prompt closes, npm install runs, user_allowed, the
     character shows 허용함, the shortcuts are unregistered again.
  4. ⌃⌥S grants the command for the session (the second run has no card);
     ⌃⌥N denies another one (denied, the model gets farero's reason, the
     character shows 거부함). Without a card a shortcut does nothing.
  5. Scenario B in the notch: railway_redeploy -> plugin card -> ⌃⌥Y ->
     user_allowed, a new deployment reaches SUCCESS.
  6. Focus: the frontmost app never changes while cards come and go, and
     the app never becomes active (기능 명세서 5-3).
  7. Without a notch (FARERO_DEBUG_NO_NOTCH=1) the island sits at the top
     centre below the screen edge and expands the same way.
  8. After /exit the island hides again, the panel leaves the screen, and
     the app is idle.
Screenshots of the island at each step are left in E2E_DIR (notch-*.png).

Environment: E2E_DIR (default /tmp/frm5), E2E_MODEL (default haiku),
E2E_NO_BUILD=1 to reuse build/dev and build/Farero.app, E2E_APP,
E2E_MANUAL=1, E2E_SKIP_B=1 to leave out scenario B (no Railway account).
Exit status is non-zero when a check fails.
"""
import importlib.util
import json
import os
import re
import signal
import stat
import subprocess
import sys
import time

from e2elib import BIN, ROOT, Abort, Harness, dialog_open, print_daemon_warnings, ready, step_register, step_remove


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), os.path.join(ROOT, "scripts", name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


P = load("e2e-plugins")  # plugin credentials, Railway target, MCP prompts
A = load("e2e-approvals")  # Bash prompts and their audit rows

E = P.E
WORK = os.path.join(ROOT, "build", "e2e-notch")
APP = os.environ.get("E2E_APP") or os.path.join(ROOT, "build", "Farero.app")
PROBE = os.path.join(BIN, "notchprobe")
MANUAL = os.environ.get("E2E_MANUAL") == "1"
SKIP_B = os.environ.get("E2E_SKIP_B") == "1"

# NotchViews.swift
EXPANDED_W, APPROVAL_W, WING = 440, 520, 46
# Idle wakeups over 10 s that still count as idle (Q80). The app has no timer
# while hidden (the hover poll alone woke it 20 times in 10 s); this leaves
# room for system events. CPU must stay under 0.5 % (0.05 s in 10 s).
IDLE_WAKEUPS, IDLE_CPU = 10, 0.05

FACTS = []
# The compact island's height: the notch (or menu bar) the cards hang below.
BAR = [38]


def fact(h, label, text):
    FACTS.append((label, text))
    h.note(f"fact {label}: {text}")


def prepare():
    P.stop_farerod_of(E)
    os.makedirs(E, exist_ok=True)
    for name in os.listdir(E):
        if name not in P.KEEP:
            subprocess.run(["rm", "-rf", os.path.join(E, name)])
    os.makedirs(os.path.join(E, "claude"))
    for name in ("alpha", "beta"):
        d = os.path.join(WORK, name)
        subprocess.run(["rm", "-rf", d])
        os.makedirs(d)
    if os.environ.get("E2E_NO_BUILD") != "1":
        print("==> build dev binaries and Farero.app", flush=True)
        if subprocess.run(["go", "build", "-p", "2", "-tags", "farero_dev", "-o", BIN + "/", "./cmd/..."],
                          cwd=os.path.join(ROOT, "daemon")).returncode != 0:
            return False
        if not os.environ.get("E2E_APP") and subprocess.run(
                [os.path.join(ROOT, "scripts", "build.sh"), "0.0.0-e2e"], cwd=ROOT).returncode != 0:
            return False
    if not os.path.exists(PROBE) or os.path.getmtime(PROBE) < os.path.getmtime(
            os.path.join(ROOT, "scripts", "notchprobe.swift")):
        if subprocess.run(["swiftc", "-O", "-o", PROBE, os.path.join(ROOT, "scripts", "notchprobe.swift")]).returncode:
            return False
    return os.path.exists(os.path.join(APP, "Contents", "MacOS", "Farero"))


# ---------------------------------------------------------------------------
# The app under test


class App:
    """Farero.app in development mode. Its stderr (FARERO_DEBUG=1) is the
    record of what it did: `farero: notch approval`, `island x,y WxH`,
    `character allowed mode compact`, `hotkey registered ⌃⌥Y`, ..."""

    def __init__(self, h, extra_env=None):
        self.h = h
        self.log_path = os.path.join(h.e, "app.log")
        self.ctl = os.path.join(h.e, "app.ctl")
        if not os.path.exists(self.ctl):
            os.mkfifo(self.ctl, 0o600)
        env = dict(os.environ, FARERO_HOME=h.e, FARERO_SOCKET=h.env["FARERO_SOCKET"], FARERO_DEBUG="1",
                   FARERO_DEBUG_CONTROL=self.ctl, **(extra_env or {}))
        self.start_pos = os.path.getsize(self.log_path) if os.path.exists(self.log_path) else 0
        log = open(self.log_path, "ab")
        self.proc = subprocess.Popen([os.path.join(APP, "Contents", "MacOS", "Farero")], env=env, stdout=log,
                                     stderr=subprocess.STDOUT, start_new_session=True)

    def pos(self):
        return os.path.getsize(self.log_path)

    def lines(self, since=None):
        with open(self.log_path, "rb") as f:
            f.seek(self.start_pos if since is None else since)
            return f.read().decode("utf-8", "replace").splitlines()

    def find(self, pattern, since=None):
        rx = re.compile(pattern)
        for line in self.lines(since):
            m = rx.search(line)
            if m:
                return m
        return None

    def wait(self, pattern, since=None, timeout=10):
        return self.h.wait(lambda: self.find(pattern, since), timeout)

    def last(self, pattern, since=None):
        rx = re.compile(pattern)
        found = None
        for line in self.lines(since):
            m = rx.search(line)
            if m:
                found = m
        return found

    def mode(self):
        m = self.last(r"farero: notch (\w+)$")
        return m and m[1]

    def island(self):
        """(x, y, w, h) of the island as the app laid it out last."""
        m = self.last(r"farero: island (-?\d+),(-?\d+) (\d+)x(\d+)")
        return m and tuple(int(v) for v in m.groups())

    def hovering(self):
        """Whether the pointer rests on the island (the person's mouse; the
        script never moves it)."""
        m = self.last(r"farero: hover (true|false)$")
        return bool(m) and m[1] == "true"

    def characters(self, since):
        return [m[1] for m in (re.search(r"farero: character (\w+)", l) for l in self.lines(since)) if m]

    def send(self, command):
        """Writes one command to the control pipe (the app reopens it after
        each writer)."""
        end = time.time() + 5
        while True:
            try:
                fd = os.open(self.ctl, os.O_WRONLY | os.O_NONBLOCK)
                break
            except OSError:
                if time.time() > end:
                    raise Abort("the app does not read its control pipe")
                self.h.sleep(0.1)
        try:
            os.write(fd, (command + "\n").encode())
        finally:
            os.close(fd)

    def windows(self):
        return [w for w in probe("windows", self.proc.pid) if w.get("onscreen")]

    def stop(self):
        if self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(5)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait(5)


def probe(*args):
    p = subprocess.run([PROBE, *map(str, args)], capture_output=True, text=True, timeout=20)
    return json.loads(p.stdout) if p.returncode == 0 and p.stdout.strip() else None


def idle_wakeups(pid, secs=10):
    """(idle wakeups, cpu seconds) of pid over secs: the difference between
    top's two samples (both columns are totals)."""
    p = subprocess.run(["top", "-l", "2", "-s", str(secs), "-stats", "pid,idlew,time", "-pid", str(pid)],
                       capture_output=True, text=True, timeout=secs + 30)
    rows = [l.split() for l in p.stdout.splitlines() if l.strip().startswith(str(pid))]
    if len(rows) < 2:
        return None, None

    def secs_of(t):  # mm:ss.cc or hh:mm:ss
        parts = [float(x) for x in re.split(r"[:]", t)]
        v = 0.0
        for x in parts:
            v = v * 60 + x
        return v
    def wakes(r):
        return int(r[1].rstrip("+"))
    return wakes(rows[-1]) - wakes(rows[0]), secs_of(rows[-1][2]) - secs_of(rows[0][2])


def shot(h, name, island, pad=12):
    """A screenshot of the island and a margin around it."""
    x, y, w, hh = island
    path = os.path.join(h.e, f"notch-{name}.png")
    subprocess.run(["screencapture", "-x", "-R", f"{x - pad},{y},{w + 2 * pad},{hh + pad}", path], timeout=20)
    return path if os.path.exists(path) else None


def island_drawn(path, island, bar, top_radius, pad=12):
    """Whether the screenshot shows the black island where the app says it
    is: the island's left margin, from below the bar to above the bottom
    corner, is black. (The menu bar here is black too, so the bar itself
    proves nothing.)"""
    try:
        from PIL import Image
    except ImportError:
        return None
    im = Image.open(path).convert("RGB")
    _, _, w, hh = island
    scale = im.size[0] / (w + 2 * pad)
    x = int((pad + top_radius + 4) * scale)
    ys = range(int((bar + 8) * scale), int((hh - 28) * scale), max(1, int(4 * scale)))
    bad = [y for y in ys if max(im.getpixel((x, y))) > 4]
    return not bad and len(ys) > 0


def front(app):
    return probe("front", app.proc.pid) or {}


# ---------------------------------------------------------------------------
# Steps


class Focus:
    """The notch must never take focus (기능 명세서 5-3): Farero is never the
    frontmost app and never active. The person at the Mac may switch apps
    during the run; that is noted, not failed (an accessory app's
    non-activating panel cannot bring another app forward)."""

    def __init__(self, h, app):
        self.h, self.app = h, app
        self.last = front(app)
        h.note(f"frontmost app: {self.last.get('name')!r} ({self.last.get('bundle')})")

    def check(self, label):
        f = front(self.app)
        self.h.check(f.get("pid") and f.get("pid") != self.app.proc.pid and not f.get("target_active"),
                     f"focus {label}: Farero is neither frontmost nor active", json.dumps(f, ensure_ascii=False))
        if f.get("pid") != self.last.get("pid"):
            self.h.note(f"frontmost app changed: {self.last.get('name')!r} -> {f.get('name')!r} (not Farero)")
        self.last = f


def step_hidden(h, app, label):
    h.section(f"hidden ({label}): no island, no window on screen, idle")
    h.check(app.wait(r"farero: recv state\.snapshot", timeout=15), "the app connected (state.snapshot)")
    h.check(h.wait(lambda: app.mode() in (None, "hidden"), 15), "notch mode hidden", f"mode {app.mode()}")
    gone = h.wait(lambda: app.windows() == [], 5)
    h.check(gone is not None and gone is not False, "the panel has no window on screen",
            json.dumps(app.windows()))
    h.sleep(1.0)
    wake, cpu = idle_wakeups(app.proc.pid)
    h.check(wake is not None and wake <= IDLE_WAKEUPS and cpu is not None and cpu <= IDLE_CPU,
            f"idle while hidden: {wake} idle wakeups (at most {IDLE_WAKEUPS}) and "
            f"{cpu if cpu is None else round(cpu, 2)} s CPU (at most {IDLE_CPU}) in 10 s")
    fact(h, "hidden", f"{label}: {wake} idle wakeups / 10 s, CPU {cpu} s")


def wait_mode(h, app, mode, since, timeout=10):
    return h.check(app.wait(rf"farero: notch {mode}$", since, timeout), f"notch mode {mode}",
                   f"last mode {app.mode()}")


def step_compact(h, app, focus):
    h.section("compact: a session starts -> the island appears around the notch, greeting")
    pos = app.pos()
    a = P.start_agent(h, "alpha")
    h.check(app.wait(r"farero: notch (compact|expanded)$", pos, 15), "the island appears",
            f"last mode {app.mode()}")
    h.check(app.wait(r"farero: panel ordered front", pos, 5), "the panel came on screen")
    h.sleep(1.0)
    win = app.windows()
    h.check(len(win) == 1 and win[0]["y"] == 0, "one on-screen panel at the top of the screen", json.dumps(win))
    chars = app.characters(pos)
    h.check("greeting" in chars, "character: greeting (인사) for the new session", f"{chars}")
    if not check_compact(h, app, notch=True):
        h.note("the pointer rests on the island: it opened by hover; the compact size is not checked")
    focus.check("after the island appeared")
    return a


def check_compact(h, app, notch):
    """Checks the compact island, unless the pointer holds it open. Returns
    whether it checked."""
    if app.hovering():
        h.check(app.mode() == "expanded", "hovered: expanded", f"mode {app.mode()}")
        return False
    h.check(app.mode() == "compact", "notch mode compact", f"mode {app.mode()}")
    isl = app.island()
    if notch:
        gap = isl and isl[2] - 2 * WING - 2 * 6
        h.check(isl and isl[1] == 0 and 22 <= isl[3] <= 40 and gap and gap > 100,
                "island: flush with the top, as tall as the notch, the notch plus a wing each side",
                f"island {isl}")
        if isl:
            BAR[0] = isl[3]
            fact(h, "compact", f"island {isl} (notch about {gap} pt wide)")
            shot(h, "compact", isl)
    else:
        h.check(isl and isl[1] == 0 and isl[2] == 2 * WING + 12 and isl[3] >= 30,
                "island: two wings, no notch gap, at least 30 pt tall", f"island {isl}")
        if isl:
            shot(h, "no-notch-compact", isl)
    return True


def check_pinned_open(h, app, name):
    """Pins the island open, checks the session list, unpins it. A click
    elsewhere closes a pinned island (the person at the Mac may click while
    the script runs): then the screenshot is skipped and noted."""
    pos = app.pos()
    app.send("pin")
    h.check(app.wait(r"farero: pinned true$", pos, 5), "pinned")
    m = app.wait(rf"farero: island (-?\d+),(-?\d+) {EXPANDED_W + 24}x(\d+)$", pos, 5)
    h.check(m and int(m[3]) > 90, f"island: the {EXPANDED_W} pt session list below the bar",
            f"last island {app.island()}")
    h.sleep(0.8)  # the spring settles
    closed = app.find(r"farero: pinned false$", pos)
    if m and not closed:
        isl = (int(m[1]), int(m[2]), EXPANDED_W + 24, int(m[3]))
        p = shot(h, name, isl)
        h.check(p and island_drawn(p, isl, BAR[0], 12), "screenshot: the island is drawn there", p)
    if closed:
        h.note("a click elsewhere closed the pinned island before the screenshot (not checked)")
    else:
        pos = app.pos()
        app.send("pin")
        h.check(app.wait(r"farero: pinned false$", pos, 5), "unpinned")
    want = "expanded" if app.hovering() else "compact"
    h.check(h.wait(lambda: app.mode() == want, 5), f"notch mode {want}"
            + (" (the pointer rests on it)" if want == "expanded" else ""), f"mode {app.mode()}")


def step_expanded(h, app):
    h.section("expanded: pinned -> the session list below the notch; unpinned -> compact")
    check_pinned_open(h, app, "expanded")


def answer(h, app, shortcut, click=False):
    """Answers the front card: through the control pipe, or by the person at
    the keyboard (E2E_MANUAL=1). Returns the log position before."""
    pos = app.pos()
    key = {"allow": "⌃⌥Y", "allow_session": "⌃⌥S", "deny": "⌃⌥N"}[shortcut]
    if MANUAL:
        what = f"click '{ {'allow': '허용', 'allow_session': '이번 세션 동안 허용', 'deny': '거부'}[shortcut]}' on the card" \
            if click else f"press {key}"
        print(f"  >>> {what} now (waiting up to 2 minutes)", flush=True)
        h.check(app.wait(rf"farero: answer \S+ {shortcut}$", pos, 120), f"the person: {what}")
        via_key = app.find(rf"farero: shortcut {re.escape(key)}", pos)
        h.check(bool(via_key) != click, f"the answer came by {'a click' if click else 'the key ' + key}")
    else:
        app.send(f"hotkey {shortcut}")
        h.check(app.wait(rf"farero: control hotkey {shortcut}: done", pos, 5), f"{key} hot-key event delivered")
        h.check(app.wait(rf"farero: shortcut {re.escape(key)}", pos, 5) and
                app.wait(rf"farero: answer \S+ {shortcut}$", pos, 5), f"{key} answered the front card")
    return pos


def card_in_notch(h, app, pos, name, focus):
    """Checks the open card is shown in the notch; returns the island."""
    wait_mode(h, app, "approval", pos)
    h.check(all(app.wait(rf"farero: hotkey registered {re.escape(k)}", pos, 5) for k in ("⌃⌥Y", "⌃⌥N")),
            "⌃⌥Y and ⌃⌥N registered while the card is open")
    h.sleep(1.0)
    isl = app.island()
    h.check(isl and isl[2] == APPROVAL_W + 24 and isl[3] >= 200, f"island: the {APPROVAL_W} pt card below the notch",
            f"island {isl}")
    if isl:
        p = shot(h, name, isl)
        h.check(p and island_drawn(p, isl, BAR[0], 12), "screenshot: the card is drawn there", p)
    chars = app.characters(pos)
    h.check("needsApproval" in chars, "character: needsApproval (승인 필요)", f"{chars}")
    focus.check("with the card open")
    return isl


def after_answer(h, app, pos, character, focus):
    h.check(app.wait(rf"farero: character {character}", pos, 10), f"character: {character}",
            f"{app.characters(pos)}")
    h.check(app.wait(r"farero: hotkey unregistered ⌃⌥Y", pos, 10), "shortcuts unregistered after the card")
    h.check(h.wait(lambda: app.mode() in ("compact", "expanded"), 10), "the island leaves approval mode",
            f"mode {app.mode()}")
    focus.check("after the answer")


def step_scenario_a(h, app, a, focus):
    h.section("scenario A in the notch: npm install -> notch card -> ⌃⌥Y -> runs")
    lock = os.path.join(a.cwd, "package-lock.json")
    pos = app.pos()
    card, _, since = A.ask(h, a, "npm install")
    if not card:
        return
    card_in_notch(h, app, pos, "card-a", focus)
    h.check(app.find(rf"farero: recv approval\.request", pos), "the app received the card")
    t_ans = time.time()
    pos = answer(h, app, "allow", click=MANUAL)
    closed = A.dialog_closed(h, a)
    h.check(closed, "the terminal prompt closes" + (f" ({closed - t_ans:.1f} s)" if closed else ""))
    h.check(h.wait(lambda: os.path.exists(lock), 60), "npm install ran (package-lock.json exists)")
    row = A.wait_row(h, "npm install", since)
    h.check(row and row["decision"] == "user_allowed" and row["reason"] == "",
            "audit log: user_allowed", row and f"decision={row['decision']!r} reason={row['reason']!r}")
    after_answer(h, app, pos, "allowed", focus)
    h.wait_status(a.sid, "waiting_input", t_ans, 60)


def step_grant_and_deny(h, app, a, focus):
    h.section("⌃⌥S: allowed for the session (no card the second time); ⌃⌥N: denied")
    log = os.path.join(a.cwd, "grant.log")
    first = "echo hit >> grant.log"
    pos = app.pos()
    card, _, since = A.ask(h, a, first)
    if not card:
        return
    card_in_notch(h, app, pos, "card-grant", focus)
    h.check(app.find(r"farero: hotkey registered ⌃⌥S", pos), "⌃⌥S registered (the card offers a session grant)")
    pos = answer(h, app, "allow_session")
    h.check(h.wait(lambda: len(A.lines(log)) == 1, 30), "the command ran once")
    row = A.wait_row(h, first, since)
    h.check(row and row["decision"] == "user_allowed" and row["reason"] == "allow_session",
            "audit log: user_allowed / allow_session", row and f"{row['decision']} / {row['reason']}")
    after_answer(h, app, pos, "allowed", focus)
    h.wait_status(a.sid, "waiting_input", time.time() - 1, 60)

    h.wait(lambda: ready(a), 30)
    since = time.time()
    pos = app.pos()
    a.type_line(f"Run this exact shell command with the Bash tool again: {first}{A.ONLY}")
    h.check(h.wait(lambda: len(A.lines(log)) == 2, 60), "the same command ran again")
    row = A.wait_row(h, first, since)
    h.check(row and row["decision"] == "session_allowed", "audit log: session_allowed",
            row and f"decision={row['decision']!r}")
    h.check(not app.find(r"farero: notch approval", pos), "no card in the notch for it")
    h.wait_status(a.sid, "waiting_input", since, 60)

    other = "echo other >> grant.log"
    pos = app.pos()
    card, _, since = A.ask(h, a, other)
    if not card:
        return
    card_in_notch(h, app, pos, "card-deny", focus)
    t_ans = time.time()
    pos = answer(h, app, "deny")
    h.check(A.dialog_closed(h, a), "the terminal prompt closes")
    row = A.wait_row(h, other, since)
    h.check(row and row["decision"] == "denied", "audit log: denied", row and f"decision={row['decision']!r}")
    after_answer(h, app, pos, "denied", focus)
    h.wait_status(a.sid, "waiting_input", t_ans, 60)
    h.check(len(A.lines(log)) == 2, "the denied command did not run", f"grant.log: {A.lines(log)}")
    h.check(h.wait(lambda: A.transcript_has(a, "farero: 사용자가 거부함"), 5), "the model got farero's reason")

    h.section("without a card the shortcuts are not registered")
    pos = app.pos()
    if not MANUAL:
        app.send("hotkey allow")
        h.check(app.wait(r"farero: control hotkey allow: ignored", pos, 5), "⌃⌥Y with no card: not registered")
        h.check(not app.find(r"farero: answer ", pos), "nothing answered")


def step_scenario_b(h, app, a, focus):
    h.section("scenario B in the notch: railway_redeploy -> plugin card -> ⌃⌥Y -> a new deployment")
    target = P.railway_target(h)
    c = P.RawClient(h)
    try:
        before, status = P.latest_deployment(c, target)
        h.note(f"newest deployment before: {before} [{status}]")
        pos = app.pos()
        since = P.ask_call(h, a, "railway_redeploy", target)
        card = P.wait_card(h, "railway", "redeploy", target, since)
        if not card:
            P.turn_end(h, a, since)
            return
        h.check(card.get("session_id") == a.sid and card.get("reasons") == ["policy"],
                "card: alpha's session, reason policy", f"{card.get('session_id')} {card.get('reasons')}")
        card_in_notch(h, app, pos, "card-b", focus)
        t_allow = time.time()
        pos = answer(h, app, "allow")
        row = P.wait_row(h, "railway", "redeploy", target, since)
        P.turn_end(h, a, since)
        h.check(row and row["decision"] == "user_allowed" and row["reason"] == "policy" and not row["error"],
                "audit log: user_allowed, reason policy, no error", P.row_desc(row))
        after_answer(h, app, pos, "allowed", focus)

        def deployed():
            dep = P.latest_deployment(c, target)
            if dep[0] and dep[0] != before and dep[1] in ("SUCCESS", "FAILED", "CRASHED"):
                return dep
            h.sleep(5.0)
            return None
        dep = h.wait(deployed, 240) or P.latest_deployment(c, target)
        h.check(dep[0] and dep[0] != before and dep[1] == "SUCCESS", "Railway: a new deployment reached SUCCESS",
                f"newest {dep[0]} [{dep[1]}], before {before}")
        fact(h, "B", f"allow -> new deployment {dep[0]} [{dep[1]}] in {time.time() - t_allow:.0f} s")
    finally:
        c.close()


def step_no_notch(h, app):
    h.section("without a notch (FARERO_DEBUG_NO_NOTCH=1): top centre, below the screen edge")
    app.stop()
    nn = App(h, {"FARERO_DEBUG_NO_NOTCH": "1"})
    try:
        h.check(nn.wait(r"farero: recv state\.snapshot", timeout=15), "the app connected")
        h.check(nn.wait(r"farero: notch (compact|expanded)$", nn.start_pos, 15), "the island appears",
                f"last mode {nn.mode()}")
        h.sleep(1.0)
        if not check_compact(h, nn, notch=False):
            h.note("the pointer rests on the island: it opened by hover; the compact size is not checked")
        check_pinned_open(h, nn, "no-notch-expanded")
    finally:
        nn.stop()
    return App(h)


def step_exit(h, app, a):
    h.section("exit the session -> the island hides")
    pos = app.pos()
    h.wait(lambda: ready(a), 30)
    a.type_line("/exit")
    h.check(h.wait(lambda: a.exited_at, 20), "alpha: claude exited after /exit")
    wait_mode(h, app, "hidden", pos, 15)
    h.check(app.wait(r"farero: panel ordered out", pos, 5), "the panel left the screen")


def main():
    if not prepare():
        print("==> E2E NOTCH FAILED (build)")
        return 1
    daemon_env = {"FARERO_GITHUB_CLIENT_ID": P.GITHUB_CLIENT_ID,
                  "FARERO_APPROVAL_TIMEOUT": "10m" if MANUAL else "2m"}
    h = Harness(E, WORK, daemon_env)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(130))
    app = None
    try:
        h.start_daemon()
        h.start_watch()
        if not SKIP_B:
            P.step_plugins(h)
        step_register(h)
        app = App(h)
        focus = Focus(h, app)
        step_hidden(h, app, "at start")
        a = step_compact(h, app, focus)
        step_expanded(h, app)
        step_scenario_a(h, app, a, focus)
        step_grant_and_deny(h, app, a, focus)
        if not SKIP_B:
            step_scenario_b(h, app, a, focus)
        app = step_no_notch(h, app)
        h.check(app.wait(r"farero: recv state\.snapshot", timeout=15), "the app reconnected")
        step_exit(h, app, a)
        step_hidden(h, app, "after the session ended")
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
        if app:
            app.stop()
        h.cleanup()
    print_daemon_warnings(E)
    if FACTS:
        print("==> facts")
        for label, text in FACTS:
            print(f"  {label}: {text}")
    print(f"==> E2E NOTCH {'FAILED' if h.failed else 'PASSED'} (logs and screenshots in {E})")
    return 1 if h.failed else 0


if __name__ == "__main__":
    sys.exit(main())
