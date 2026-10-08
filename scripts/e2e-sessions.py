#!/usr/bin/env python3
"""End-to-end check of session watching (M2) with a real, interactive Claude Code.

    scripts/e2e-sessions.py

Two `claude` sessions (folders `alpha` and `beta` under build/e2e-sessions)
run at the same time (a third, `gamma`, only starts and is killed). The
script types into them like a user (prompts, Esc, the terminal permission
prompt, /exit). The harness (pseudo-terminals, farero-devctl watch as the
app, isolation from the developer's ~/.claude) is scripts/e2elib.py.

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
build/dev (E2E_BIN: another binary folder). Uses python3's standard library only. Exit status is non-zero when
a check fails; logs, pty transcripts and screens are left in E2E_DIR.
"""
import json
import os
import re
import signal
import sys
import time

from e2elib import (ENTER, ESC, ROOT, Abort, Agent, Harness, descendants, find_row, prepare,
                    print_daemon_warnings, ready, select_option, selected_option, step_register, step_remove,
                    wait_card)

E = os.environ.get("E2E_DIR", "/tmp/frm2")
WORK = os.path.join(ROOT, "build", "e2e-sessions")


# ---------------------------------------------------------------------------
# Steps


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
    sel = select_option(h, beta, "No")
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


def main():
    if not prepare(E, WORK, ("alpha", "beta", "gamma")):
        return 1

    h = Harness(E, WORK)
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

    print_daemon_warnings(E)
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
