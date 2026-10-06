#!/usr/bin/env python3
"""End-to-end check of agent tool approval (M3, 기능 명세서 F-04) with a real,
interactive Claude Code.

    scripts/e2e-approvals.py

One `claude` session (folder `alpha` under build/e2e-approvals, an empty npm
project) asks for Bash permissions; the script answers the approval cards
like the app would (`farero-devctl answer`), or the terminal prompt like a
user. A second session (`beta`) runs with a model that does not exist, for
StopFailure. The harness (pseudo-terminals, farero-devctl watch as the app,
isolation from the developer's ~/.claude) is scripts/e2elib.py.

What it checks (MVP 구현 순서 M3 완료 기준):
  1. Scenario A: `npm install` asks -> card (agent tool, reason, allow for
     session offered, input, 10 minute deadline) and the terminal prompt ->
     allowed on the card -> the terminal prompt closes, npm runs, log row
     `user_allowed`.
  2. Session grants are per command (Q38): "allow for this session" on one
     command lets the same command run again without a card; another
     command asks again and a deny on the card stops it (log `denied`).
  3. "Yes" in the terminal, then deny on the card while the tool runs: the
     tool runs (Claude Code ignores the late answer) and the log row ends as
     `cancelled`/`answered_in_agent`, not `denied`.
  4. No answer until the deadline: the card is withdrawn (`timeout`), the
     hook denies, the command does not run, log row `timeout`. The deadline
     is E2E_APPROVAL_TIMEOUT (default 30s, via farerod --dev's
     FARERO_APPROVAL_TIMEOUT); E2E_APPROVAL_TIMEOUT=10m runs only this step
     with the real deadline (use another E2E_DIR to run it next to the
     short run).
  5. App not running: no card, the terminal's own prompt answers, log row
     `passthrough`/`app_not_running`.
  6. An API error ends the turn with StopFailure -> waiting_input.

Environment: E2E_DIR (default /tmp/frm3; keep it short, the socket path must
stay under 104 bytes), E2E_MODEL (default haiku), E2E_APPROVAL_TIMEOUT,
E2E_NO_BUILD=1 to reuse build/dev (E2E_BIN: another binary folder). Uses python3's standard library only.
Exit status is non-zero when a check fails; logs, pty transcripts and
screens are left in E2E_DIR.
"""
import glob
import json
import os
import re
import signal
import sys
import time

from e2elib import (ENTER, ROOT, Abort, Agent, Harness, dialog_open, prepare, print_daemon_warnings, ready,
                    selected_option, step_register, step_remove, wait_card)

def parse_duration(s):
    m = re.fullmatch(r"(\d+)(s|m)", s.strip())
    if not m:
        raise SystemExit(f"E2E_APPROVAL_TIMEOUT: want e.g. 30s or 10m, got {s!r}")
    return int(m[1]) * (60 if m[2] == "m" else 1)


TIMEOUT_TEXT = os.environ.get("E2E_APPROVAL_TIMEOUT", "30s")
TIMEOUT = parse_duration(TIMEOUT_TEXT)
REAL_TIMEOUT = TIMEOUT == 600

E = os.environ.get("E2E_DIR", "/tmp/frm3")
# The 10 minute run has its own folders, so it can run next to the short one.
WORK = os.path.join(ROOT, "build", "e2e-approvals" + ("-10m" if REAL_TIMEOUT else ""))


# ---------------------------------------------------------------------------
# Helpers


def start_agent(h, name, args=()):
    since = time.time()
    a = Agent(h, name, args)
    h.agents.append(a)
    ev = h.wait(lambda: h.find_event("session.updated", lambda d: d.get("pid") == a.proc.pid, since), 60)
    ok = ev and h.wait(lambda: ready(a), 30)
    a.save_screen("started")
    if not h.check(ok, f"{a.name}: started, SessionStart reached farerod"):
        raise Abort(f"{a.name} did not start")
    a.sid = ev["data"]["id"]
    h.sleep(1.0)
    return a


def rows_for(h, command, since):
    """Agent-tool log rows for a Bash command since a time, oldest first."""
    return [r for r in h.db_calls(since) if r["kind"] == "agent" and r["tool"] == "Bash" and command in r["input"]]


def wait_row(h, command, since, pred=lambda r: True, timeout=10):
    def hit():
        rows = [r for r in rows_for(h, command, since) if pred(r)]
        return rows[-1] if rows else None
    return h.wait(hit, timeout)


# Keeps the model from trying something else after a deny.
ONLY = " Run nothing else; if it is not allowed, just say so."


def ask(h, a, command):
    """Asks claude to run command; returns (card, its time, start time)."""
    h.wait(lambda: ready(a), 30)
    since = time.time()
    a.type_line(f"Run this exact shell command with the Bash tool: {command}{ONLY}")
    card, t = wait_card(h, a, since, command)
    return card, t, since


def transcript_has(a, text):
    """Whether the session's transcript (Claude Code's own record of what the
    model saw) contains text."""
    sid = a.sid.split(":", 1)[-1]
    for path in glob.glob(os.path.expanduser(f"~/.claude/projects/*/{sid}.jsonl")):
        with open(path, encoding="utf-8") as f:
            if text in f.read():
                return True
    return False


def lines(path):
    try:
        with open(path) as f:
            return f.read().splitlines()
    except OSError:
        return []


def still_refused(h, a, command, since, decision, t_answer):
    """After the turn, a refusal the agent followed is still logged as such
    (no PostToolUse for that tool use)."""
    evs = [ev for ts, ev, tool in h.hook_events(a.sid) if ts >= t_answer and ev.startswith("PostToolUse")]
    h.note(f"PostToolUse(Failure) events after farero's answer: {evs or 'none'}")
    rows = rows_for(h, command, since)
    h.check(rows and rows[-1]["decision"] == decision, f"audit log after the turn: still {decision}",
            rows and f"decision={rows[-1]['decision']!r} reason={rows[-1]['reason']!r}")


def yes_selected(h, a):
    """Waits for the terminal prompt's first option, "Yes", to be selected.
    Right after the prompt opens, a spinner can still cover that line."""
    sel = h.wait(lambda: (lambda o: o if o and o[1].startswith("Yes") else None)(selected_option(a)), 10)
    return h.check(sel, "'Yes' selected in the terminal prompt", f"selected {selected_option(a)}")


def dialog_closed(h, a, timeout=10):
    """When the terminal prompt closed (None if it did not)."""
    return h.wait(lambda: not dialog_open(a) and time.time(), timeout)


# ---------------------------------------------------------------------------
# Steps


def step_scenario_a(h, a):
    h.section("scenario A: npm install -> card -> allowed in the app -> runs")
    lock = os.path.join(a.cwd, "package-lock.json")
    card, t_card, since = ask(h, a, "npm install")
    if not card:
        return
    h.check(card.get("kind") == "agent" and card.get("tool") == "Bash" and card.get("plugin") == "",
            "card: agent tool Bash", json.dumps(card, ensure_ascii=False)[:300])
    h.check(card.get("reasons") == ["agent_request"] and card.get("allow_session") is True,
            "card: reason agent_request, 'allow for this session' offered",
            f"reasons={card.get('reasons')} allow_session={card.get('allow_session')}")
    inp = card.get("input") or {}
    h.check(inp.get("command") == "npm install" and "description" in inp,
            "card: the whole tool input (command and description)", json.dumps(inp, ensure_ascii=False))
    h.check(card.get("session_id") == a.sid and card.get("session_label") == "alpha",
            "card: session alpha", f"{card.get('session_id')} / {card.get('session_label')!r}")
    dl = deadline_secs(card)
    h.check(dl is not None and abs(dl - TIMEOUT) < 2, f"card: deadline {TIMEOUT_TEXT} after it was created",
            f"deadline - created_at = {dl}")
    h.check(h.wait(lambda: h.status(a.sid) == "waiting_approval", 3), "alpha: waiting_approval")
    h.sleep(1.0)
    t_ans = time.time()
    h.check(h.answer(card["id"], "allow") == "ok", "answered allow on the card")
    closed = dialog_closed(h, a)
    h.check(closed, "the terminal prompt closes" + (f" ({closed - t_ans:.1f} s)" if closed else ""))
    h.check(h.wait(lambda: os.path.exists(lock), 60), "npm install ran (package-lock.json exists)")
    row = wait_row(h, "npm install", since)
    h.check(row and row["decision"] == "user_allowed" and row["reason"] == "",
            "audit log: user_allowed", row and f"decision={row['decision']!r} reason={row['reason']!r}")
    tw = h.wait_status(a.sid, "waiting_input", t_ans, 60)
    seq = [st for _, st, _ in h.history(a.sid, t_ans)]
    h.check(tw and "running" in seq, "alpha: running after the answer, then waiting_input", f"statuses: {seq}")
    a.save_screen("after scenario A")


def deadline_secs(card):
    def parse(s):
        m = re.match(r"(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(\.\d+)?(Z|[+-]\d\d:\d\d)", s or "")
        if not m:
            return None
        t = time.mktime((int(m[1]), int(m[2]), int(m[3]), int(m[4]), int(m[5]), int(m[6]), 0, 0, 0))
        return t + float(m[7] or 0)  # both stamps share the zone
    c, d = parse(card.get("created_at")), parse(card.get("deadline"))
    return d - c if c is not None and d is not None else None


def step_session_grant(h, a):
    h.section("session grant is per command (Q38)")
    log = os.path.join(a.cwd, "grant.log")
    first = "echo hit >> grant.log"
    card, _, since = ask(h, a, first)
    if not card:
        return
    h.check(h.answer(card["id"], "allow_session") == "ok", "answered 'allow for this session'")
    h.check(h.wait(lambda: len(lines(log)) == 1, 30), "the command ran once")
    row = wait_row(h, first, since)
    h.check(row and row["decision"] == "user_allowed" and row["reason"] == "allow_session",
            "audit log: user_allowed / allow_session", row and f"{row['decision']} / {row['reason']}")
    h.wait_status(a.sid, "waiting_input", time.time() - 1, 60)

    h.wait(lambda: ready(a), 30)
    since = time.time()
    a.type_line(f"Run this exact shell command with the Bash tool again: {first}{ONLY}")
    h.check(h.wait(lambda: len(lines(log)) == 2, 60), "the same command ran again")
    row = wait_row(h, first, since)
    h.check(row and row["decision"] == "session_allowed", "audit log: session_allowed",
            row and f"decision={row['decision']!r}")
    asked = h.find_event("approval.request", lambda d: d.get("session_id") == a.sid, since)
    h.check(not asked and not dialog_open(a), "no card and no terminal prompt for it")
    h.wait_status(a.sid, "waiting_input", since, 60)

    other = "echo other >> grant.log"
    card, _, since = ask(h, a, other)
    if not card:
        return
    h.check(card.get("allow_session") is True, "another command asks again")
    t_ans = time.time()
    h.check(h.answer(card["id"], "deny") == "ok", "answered deny on the card")
    h.check(dialog_closed(h, a), "the terminal prompt closes")
    row = wait_row(h, other, since)
    h.check(row and row["decision"] == "denied", "audit log: denied", row and f"decision={row['decision']!r}")
    tw = h.wait_status(a.sid, "waiting_input", t_ans, 60)
    h.wait(lambda: ready(a), 10)
    h.check(tw and len(lines(log)) == 2, "the denied command did not run", f"grant.log: {lines(log)}")
    a.save_screen("after deny")
    still_refused(h, a, other, since, "denied", t_ans)
    h.check(h.wait(lambda: transcript_has(a, "farero: 사용자가 거부함"), 5), "the model got farero's reason")


def step_late_deny(h, a):
    h.section("Yes in the terminal, then deny on the card while the tool runs")
    done = os.path.join(a.cwd, "late.txt")
    command = "python3 -c 'import time; time.sleep(8)' && touch late.txt"
    card, _, since = ask(h, a, command)
    if not card:
        return
    if not yes_selected(h, a):
        return
    a.send(ENTER)
    h.sleep(2.0)
    open_card = not h.find_event("approval.cancelled", lambda d: d.get("approval_id") == card["id"], since)
    h.check(open_card, "the hook still waits while the approved tool runs (card open)")
    h.check(h.answer(card["id"], "deny") == "ok", "answered deny on the card")
    row = wait_row(h, command, since)
    h.check(row and row["decision"] == "denied", "audit log right after the answer: denied",
            row and f"decision={row['decision']!r}")
    h.check(h.wait(lambda: os.path.exists(done), 30), "the tool ran anyway (late.txt exists)")
    row = wait_row(h, command, since, lambda r: r["decision"] != "denied", 10)
    h.check(row and row["decision"] == "cancelled" and row["reason"] == "answered_in_agent",
            "audit log after PostToolUse: cancelled / answered_in_agent",
            row and f"decision={row['decision']!r} reason={row['reason']!r}")
    h.check(len(rows_for(h, command, since)) == 1, "one row for the tool use")
    h.wait_status(a.sid, "waiting_input", since, 60)


def step_timeout(h, a):
    h.section(f"no answer until the deadline ({TIMEOUT_TEXT})")
    path = os.path.join(a.cwd, "timeout.txt")
    card, t_card, since = ask(h, a, "touch timeout.txt")
    if not card:
        return
    c = h.wait_event("approval.cancelled", lambda d: d.get("approval_id") == card["id"], since, TIMEOUT + 30)
    took = c and c["t"] - t_card
    h.check(c and c["data"].get("reason") == "timeout" and abs(took - TIMEOUT) < 5,
            "the card is withdrawn with reason timeout at the deadline"
            + (f" ({took:.1f} s)" if c else ""), c and json.dumps(c["data"]))
    closed = dialog_closed(h, a, 15)
    h.check(closed, "the hook denied: the terminal prompt closes")
    row = wait_row(h, "touch timeout.txt", since)
    h.check(row and row["decision"] == "timeout", "audit log: timeout", row and f"decision={row['decision']!r}")
    h.wait_status(a.sid, "waiting_input", since, 60)
    h.wait(lambda: ready(a), 10)
    a.save_screen("after timeout")
    h.check(not os.path.exists(path), "the command did not run")
    h.check(h.wait(lambda: transcript_has(a, "farero: 승인 대기 시간 초과"), 5), "the model got farero's reason")
    still_refused(h, a, "touch timeout.txt", since, "timeout", c["t"] if c else since)


def step_app_off(h, a):
    h.section("app not running: the terminal prompt answers")
    path = os.path.join(a.cwd, "app-off.txt")
    h.stop_watch()
    h.sleep(0.5)
    h.wait(lambda: ready(a), 30)
    since = time.time()
    a.type_line(f"Run this exact shell command with the Bash tool: touch app-off.txt{ONLY}")
    row = h.wait(lambda: (rows_for(h, "touch app-off.txt", since) or [None])[-1], 60)
    h.check(row and row["decision"] == "passthrough" and row["reason"] == "app_not_running",
            "audit log: passthrough / app_not_running (the hook answered nothing)",
            row and f"decision={row['decision']!r} reason={row['reason']!r}")
    ok = h.wait(lambda: dialog_open(a), 15)
    a.save_screen("app off prompt")
    h.check(ok, "the terminal shows its own permission prompt")
    if yes_selected(h, a):
        a.send(ENTER)
        h.check(h.wait(lambda: os.path.exists(path), 30), "allowed in the terminal: the command ran")
    h.start_watch()
    h.wait(lambda: ready(a), 60)


def step_stop_failure(h):
    h.section("an API error ends the turn with StopFailure -> waiting_input")
    b = start_agent(h, "beta", ["--model", "claude-nonexistent-farero"])
    since = time.time()
    b.type_line("Reply with just the word hi.")
    tr = h.wait_status(b.sid, "running", since, 30)
    tw = tr and h.wait_status(b.sid, "waiting_input", tr, 60)
    evs = [ev for ts, ev, _ in h.hook_events(b.sid) if ts >= since]
    b.save_screen("after StopFailure")
    h.check("StopFailure" in evs and "Stop" not in evs, "beta: the turn ended with StopFailure", f"events: {evs}")
    h.check(tw, "beta: running -> waiting_input")


def step_exit(h):
    h.section("exit the sessions")
    for a in h.agents:
        if a.exited_at:
            continue
        h.wait(lambda: ready(a), 30)
        a.type_line("/exit")
        h.check(h.wait(lambda: a.exited_at, 20), f"{a.name}: claude exited after /exit")


def main():
    if not prepare(E, WORK, ("alpha", "beta")):
        return 1
    alpha_dir = os.path.join(WORK, "alpha")
    with open(os.path.join(alpha_dir, "package.json"), "w") as f:
        json.dump({"name": "farero-e2e", "version": "1.0.0", "private": True}, f)
    with open(os.path.join(alpha_dir, ".npmrc"), "w") as f:
        f.write("audit=false\nfund=false\nupdate-notifier=false\n")

    daemon_env = {} if REAL_TIMEOUT else {"FARERO_APPROVAL_TIMEOUT": TIMEOUT_TEXT}
    h = Harness(E, WORK, daemon_env)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(130))
    try:
        h.start_daemon()
        step_register(h)
        h.start_watch()
        if REAL_TIMEOUT:
            a = start_agent(h, "alpha")
            step_timeout(h, a)
        else:
            a = start_agent(h, "alpha")
            step_scenario_a(h, a)
            step_session_grant(h, a)
            step_late_deny(h, a)
            step_timeout(h, a)
            step_app_off(h, a)
            step_stop_failure(h)
        step_exit(h)
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
    print(f"==> E2E APPROVALS {'FAILED' if h.failed else 'PASSED'} (logs in {E})")
    return 1 if h.failed else 0


if __name__ == "__main__":
    sys.exit(main())
