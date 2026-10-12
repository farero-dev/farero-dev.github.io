#!/bin/bash
# Checks for the clean-account release check (docs/release-checklist.md, part b).
#
#   release-check.sh <step> [version]
#
# Run each step in the test account's Terminal when the checklist says so:
#   pre           before installing: Claude Code present, no trace of farero
#   download      after unzipping in ~/Downloads: checksum, quarantine, signature
#   translocated  while Farero.app runs from ~/Downloads: nothing registered
#   installed     after the first launch from /Applications: LaunchAgent, socket, gateway
#   registered    after registering with Claude Code: hooks, allow rule, MCP server
#   log           after scenarios A-D: audit log rows and the decisions they need,
#                 and no token in the log, the database or the Claude Code files
#   reboot        after a restart and logging in again: farerod came back by itself
#   removed       after 설정 제거: Claude Code files without farero
#   uninstalled   after the README uninstall: no trace left
#
# It only reads. It never changes the install, the Claude Code files or the
# Keychain (Keychain lookups read item attributes, not secrets, so nothing
# prompts). Every result line is also appended to a report in /Users/Shared
# (FARERO_CHECK_REPORT), where the developer's own account can read it.
# Exit status is 1 when a check fails.
set -uo pipefail

APP=/Applications/Farero.app
LABEL=dev.farero.farerod
SERVICE="gui/$(id -u)/$LABEL"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
KEYCHAIN_SERVICE=dev.farero.farerod
BUNDLE_ID=dev.farero.Farero
SUPPORT="$HOME/Library/Application Support/Farero"
DB="$SUPPORT/farero.db"
SOCK="$SUPPORT/farerod.sock"
LOGS="$HOME/Library/Logs/Farero"
if [ -n "${CLAUDE_CONFIG_DIR:-}" ]; then
  SETTINGS="$CLAUDE_CONFIG_DIR/settings.json"
  CLAUDE_JSON="$CLAUDE_CONFIG_DIR/.claude.json"
else
  SETTINGS="$HOME/.claude/settings.json"
  CLAUDE_JSON="$HOME/.claude.json"
fi
MIN_CLAUDE=2.1.203
HOOK_EVENTS="SessionStart UserPromptSubmit PreToolUse PostToolUse PostToolUseFailure PermissionRequest Notification Stop StopFailure SessionEnd"
REPORT="${FARERO_CHECK_REPORT:-/Users/Shared/farero-check-$(id -un).md}"
FAILS=0

out() { echo "$1"; echo "$1" >> "$REPORT" 2>/dev/null; }
ok() { out "  ok   $1"; }
fail() { out "  FAIL $1"; FAILS=$((FAILS + 1)); }
warn() { out "  warn $1"; }
info() { out "       $1"; }
# check <message> <command...>: ok when the command succeeds.
check() { local msg=$1; shift; if "$@" >/dev/null 2>&1; then ok "$msg"; else fail "$msg"; fi; }
# absent <message> <command...>: ok when the command fails.
absent() { local msg=$1; shift; if "$@" >/dev/null 2>&1; then fail "$msg"; else ok "$msg"; fi; }

# js <file> <expression> [arg]: evaluates a JavaScript expression over the
# parsed JSON file `j` (with `a` = arg) and prints the result. Prints
# __NOFILE__ or __BADJSON__ instead when the file is missing or not JSON.
# Uses JavaScript for Automation because a fresh account has no jq or python.
js() {
  osascript -l JavaScript - "$1" "$2" "${3:-}" 2>/dev/null <<'EOF'
function run(argv) {
  ObjC.import('Foundation');
  var s = $.NSString.stringWithContentsOfFileEncodingError(argv[0], $.NSUTF8StringEncoding, null);
  if (s.isNil()) return '__NOFILE__';
  var j;
  try { j = JSON.parse(ObjC.unwrap(s)); } catch (e) { return '__BADJSON__'; }
  return String(new Function('j', 'a', 'return (' + argv[1] + ')')(j, argv[2]));
}
EOF
}

# ver_ge A B: dotted version A >= B (numeric parts, suffixes ignored).
ver_ge() {
  local IFS=. i x y
  local a=($1) b=($2)
  for i in 0 1 2; do
    x=${a[$i]:-0}; y=${b[$i]:-0}
    x=${x%%[!0-9]*}; y=${y%%[!0-9]*}
    [ "${x:-0}" -gt "${y:-0}" ] && return 0
    [ "${x:-0}" -lt "${y:-0}" ] && return 1
  done
  return 0
}

app_version() { /usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$1/Contents/Info.plist" 2>/dev/null; }
farerod_pid() { launchctl print "$SERVICE" 2>/dev/null | awk '$1 == "pid" && $2 == "=" { print $3; exit }'; }
# gateway_listen: the TCP addresses farerod listens on.
gateway_listen() {
  local pid
  pid=$(farerod_pid)
  [ -n "$pid" ] && lsof -nP -a -p "$pid" -iTCP -sTCP:LISTEN -Fn 2>/dev/null | sed -n 's/^n//p'
}
# start_epoch <pid>: the process start time in seconds since the epoch.
start_epoch() {
  local s
  s=$(LC_ALL=C ps -o lstart= -p "$1" 2>/dev/null | sed 's/ *$//')
  [ -n "$s" ] && LC_ALL=C date -j -f '%a %b %d %T %Y' "$s" +%s 2>/dev/null
}
fmt_time() { date -r "$1" '+%Y-%m-%d %H:%M:%S'; }
q() { sqlite3 -readonly "$DB" "$1" 2>/dev/null; }

# Claude Code files hold nothing of farero.
claude_clean() {
  if [ -f "$SETTINGS" ]; then
    absent "settings.json has no farero-hook entry" grep -q farero-hook "$SETTINGS"
    absent "settings.json has no mcp__farero__* rule" grep -q 'mcp__farero__' "$SETTINGS"
  else
    ok "no settings.json ($SETTINGS)"
  fi
  local m
  m=$(js "$CLAUDE_JSON" '!!(j.mcpServers && j.mcpServers.farero)')
  case "$m" in
    __NOFILE__) ok "no ~/.claude.json" ;;
    false) ok "no user-scope MCP server farero" ;;
    __BADJSON__) fail "~/.claude.json is not valid JSON" ;;
    *) fail "no user-scope MCP server farero (found one in $CLAUDE_JSON)" ;;
  esac
}

# No file, job, Keychain item or preference of farero is left.
no_trace() {
  absent "no farerod job in launchd" launchctl print "$SERVICE"
  absent "no LaunchAgent plist" test -e "$PLIST"
  absent "no $APP" test -e "$APP"
  absent "no Application Support/Farero" test -e "$SUPPORT"
  absent "no Logs/Farero" test -e "$LOGS"
  absent "no Keychain item of $KEYCHAIN_SERVICE" security find-generic-password -s "$KEYCHAIN_SERVICE"
  absent "no preferences ($BUNDLE_ID)" defaults read "$BUNDLE_ID"
  absent "no preferences plist" test -e "$HOME/Library/Preferences/$BUNDLE_ID.plist"
  claude_clean
  local p
  for p in "Caches/$BUNDLE_ID" "HTTPStorages/$BUNDLE_ID" "HTTPStorages/$BUNDLE_ID.binarycookies" \
    "Saved Application State/$BUNDLE_ID.savedState" "WebKit/$BUNDLE_ID"; do
    [ -e "$HOME/Library/$p" ] && warn "~/Library/$p is left (not covered by the README uninstall steps)"
  done
  if launchctl print-disabled "gui/$(id -u)" 2>/dev/null | grep -q "\"$LABEL\""; then
    info "launchd still remembers $LABEL in print-disabled (harmless; the system keeps it)"
  fi
  return 0
}

# The daemon runs from /Applications under launchd, and its socket and
# gateway are up.
daemon_up() {
  local pid cmd mode listen
  pid=$(farerod_pid)
  if [ -z "$pid" ]; then
    fail "farerod is running under launchd ($SERVICE)"
    launchctl print "$SERVICE" 2>&1 | grep -E 'state|last exit' | sed 's/^/       /'
    return
  fi
  ok "farerod is running under launchd (pid $pid)"
  cmd=$(ps -o command= -p "$pid")
  [ "$cmd" = "$APP/Contents/MacOS/farerod" ] && ok "farerod runs from $APP" || fail "farerod runs from $APP (it runs $cmd)"
  if launchctl print-disabled "gui/$(id -u)" 2>/dev/null | grep -q "\"$LABEL\" => disabled"; then
    fail "farerod is allowed in Login Items (it is turned off)"
  else
    ok "farerod is not turned off in Login Items"
  fi
  if [ -S "$SOCK" ]; then
    mode=$(stat -f %Lp "$SOCK")
    [ "$mode" = 600 ] && ok "socket exists with mode 0600" || fail "socket mode is 0600 (it is $mode)"
  else
    fail "socket exists ($SOCK)"
  fi
  listen=$(gateway_listen)
  if [ -z "$listen" ]; then
    fail "gateway is listening (if a Keychain prompt is open, answer it and run this again)"
  elif echo "$listen" | grep -qv '^127\.0\.0\.1:'; then
    fail "gateway listens only on 127.0.0.1 (it listens on $(echo $listen))"
  else
    ok "gateway listens on $(echo $listen)"
  fi
}

header() {
  echo >> "$REPORT" 2>/dev/null
  echo "## $1 — $(date '+%Y-%m-%d %H:%M:%S') — $(id -un)@$(hostname -s)" >> "$REPORT" 2>/dev/null
  echo "==> $1"
}

step_pre() {
  header "pre: before installing"
  info "macOS $(sw_vers -productVersion) $(sw_vers -buildVersion), $(uname -m), $(sysctl -n machdep.cpu.brand_string 2>/dev/null)"
  ver_ge "$(sw_vers -productVersion)" 15 && ok "macOS 15 or later" || fail "macOS 15 or later"
  if id -Gn | tr ' ' '\n' | grep -qx admin; then
    warn "$(id -un) is an administrator (the checklist uses a standard account)"
  else
    ok "$(id -un) is a standard account"
  fi
  check "Terminal.app exists" test -d /System/Applications/Utilities/Terminal.app
  local v
  if command -v claude >/dev/null; then
    v=$(claude --version 2>/dev/null | awk '{ print $1; exit }')
    info "claude $v at $(command -v claude)"
    ver_ge "$v" "$MIN_CLAUDE" && ok "Claude Code $MIN_CLAUDE or later" || fail "Claude Code $MIN_CLAUDE or later (it is $v)"
  else
    fail "claude is on PATH"
  fi
  no_trace
}

step_download() {
  header "download: the zip from Releases"
  local dl="$HOME/Downloads" zip ver app bin archs
  if [ -n "${1:-}" ]; then
    zip="$dl/Farero-$1.zip"
  else
    zip=$(ls -t "$dl"/Farero-*.zip 2>/dev/null | head -1)
  fi
  if [ -z "$zip" ] || [ ! -f "$zip" ]; then
    fail "Farero-<version>.zip is in ~/Downloads"
    return
  fi
  ver=$(basename "$zip" .zip); ver=${ver#Farero-}
  info "zip: $zip ($(stat -f %z "$zip") bytes), version $ver"
  if [ -f "$zip.sha256" ]; then
    check "checksum matches $(basename "$zip").sha256" sh -c "cd \"$dl\" && shasum -a 256 -c \"$(basename "$zip").sha256\""
  else
    fail "$(basename "$zip").sha256 is in ~/Downloads"
  fi
  if xattr -p com.apple.quarantine "$zip" >/dev/null 2>&1; then
    ok "zip has the quarantine attribute (downloaded with a browser)"
  else
    warn "zip has no quarantine attribute, so Gatekeeper and translocation are not tested (download it with Safari or Chrome)"
  fi
  app="$dl/Farero.app"
  if [ ! -d "$app" ]; then
    info "unzip it (double-click) and run this step again to check the app"
    return
  fi
  if xattr -p com.apple.quarantine "$app" >/dev/null 2>&1; then
    ok "unzipped Farero.app has the quarantine attribute"
  else
    warn "unzipped Farero.app has no quarantine attribute"
  fi
  [ "$(app_version "$app")" = "$ver" ] && ok "app version is $ver" || fail "app version is $ver (it is $(app_version "$app"))"
  check "signature verifies (codesign --deep --strict)" codesign --verify --deep --strict "$app"
  for bin in Farero farerod farero-hook; do
    archs=$(lipo -archs "$app/Contents/MacOS/$bin" 2>/dev/null)
    if [[ " $archs " == *" x86_64 "* && " $archs " == *" arm64 "* ]]; then
      ok "$bin is universal ($archs)"
    else
      fail "$bin is universal (it is: $archs)"
    fi
  done
  info "Gatekeeper: $(spctl --assess --type execute "$app" 2>&1 | tr '\n' ' ') (rejected is expected: not notarized)"
}

step_translocated() {
  header "translocated: Farero.app opened from ~/Downloads"
  local line app_path
  line=$(ps -axo pid=,command= | grep '/Farero\.app/Contents/MacOS/Farero$' | grep -v grep | head -1)
  app_path=${line#*[0-9] }
  if [ -z "$line" ]; then
    fail "Farero is running (open ~/Downloads/Farero.app and run this again)"
  else
    case "$app_path" in
      */AppTranslocation/*) ok "Farero runs from a translocated copy" ;;
      *) warn "Farero does not run translocated ($app_path); the quarantine attribute may be missing" ;;
    esac
    info "path: $app_path"
  fi
  absent "no LaunchAgent plist written" test -e "$PLIST"
  absent "no farerod job loaded" launchctl print "$SERVICE"
  claude_clean
}

step_installed() {
  header "installed: first launch from /Applications"
  if [ ! -d "$APP" ]; then
    fail "$APP exists"
    return
  fi
  local ver arg0 rv
  ver=$(app_version "$APP")
  info "$APP version $ver"
  if xattr -p com.apple.quarantine "$APP" >/dev/null 2>&1; then
    info "quarantine attribute: $(xattr -p com.apple.quarantine "$APP")"
  fi
  if [ -f "$PLIST" ]; then
    arg0=$(plutil -extract ProgramArguments.0 raw -o - "$PLIST" 2>/dev/null)
    [ "$arg0" = "$APP/Contents/MacOS/farerod" ] && ok "LaunchAgent plist points at $APP" || fail "LaunchAgent plist points at $APP (it points at $arg0)"
  else
    fail "LaunchAgent plist exists ($PLIST)"
  fi
  daemon_up
  rv=$("$APP/Contents/MacOS/farerod" --version 2>/dev/null | awk '{ print $NF; exit }')
  [ "$rv" = "$ver" ] && ok "farerod --version is $ver" || fail "farerod --version is $ver (it is $rv)"
  check "gateway secret is in the Keychain" security find-generic-password -s "$KEYCHAIN_SERVICE" -a gateway.secret
  if ps -axo command= | grep -q "^$APP/Contents/MacOS/Farero$"; then
    ok "Farero.app is running"
  else
    warn "Farero.app is not running"
  fi
  if [ -f "$LOGS/farerod.log" ]; then
    info "farerod.log: $(wc -l < "$LOGS/farerod.log" | tr -d ' ') lines; last lines with 'error':"
    grep -i error "$LOGS/farerod.log" | tail -3 | cut -c1-200 | sed 's/^/         /' | tee -a "$REPORT" 2>/dev/null
  fi
}

step_registered() {
  header "registered: farero in Claude Code"
  local hook="$APP/Contents/MacOS/farero-hook" ev r missing="" entry others listen
  [ -f "$SETTINGS" ] || { fail "settings.json exists ($SETTINGS)"; return; }
  for ev in $HOOK_EVENTS; do
    r=$(js "$SETTINGS" "((j.hooks||{})['$ev']||[]).some(function(m){return (m.hooks||[]).some(function(h){return h.command===a && h.timeout===660})})" "\"$hook\" --agent claude")
    [ "$r" = true ] || missing="$missing $ev"
  done
  if [ -z "$missing" ]; then
    ok "hooks for all 10 events run $hook with timeout 660"
  else
    fail "hooks for all 10 events run $hook with timeout 660 (missing:$missing)"
  fi
  r=$(js "$SETTINGS" "((j.permissions||{}).allow||[]).indexOf('mcp__farero__*')>=0")
  [ "$r" = true ] && ok "permissions.allow has mcp__farero__*" || fail "permissions.allow has mcp__farero__*"
  others=$(js "$SETTINGS" "((j.hooks||{}).PermissionRequest||[]).reduce(function(n,m){return n+(m.hooks||[]).filter(function(h){return (h.command||'').indexOf('farero-hook')<0}).length},0)")
  [ "$others" = 0 ] || warn "$others other PermissionRequest hook(s) in settings.json can override farero's answer"

  entry=$(js "$CLAUDE_JSON" "JSON.stringify((j.mcpServers||{}).farero||null)")
  info "MCP server farero: $entry"
  r=$(js "$CLAUDE_JSON" "(function(s){return !!s && s.type==='http' && /^http:\/\/127\.0\.0\.1:[0-9]+\/mcp$/.test(s.url) && s.headersHelper===a && s.timeout===660000})((j.mcpServers||{}).farero)" "\"$hook\" --headers --agent claude")
  [ "$r" = true ] && ok "MCP server farero: http on 127.0.0.1, headersHelper, timeout 660000" || fail "MCP server farero: http on 127.0.0.1, headersHelper, timeout 660000"
  listen=$(gateway_listen | head -1)
  if [ -z "$listen" ]; then
    fail "gateway is listening (run the installed step)"
  else
    case "$entry" in
      *"$listen/mcp"*) ok "MCP server URL uses the gateway's address ($listen)" ;;
      *) fail "MCP server URL uses the gateway's address ($listen)" ;;
    esac
  fi
  if ls "$SUPPORT/backups"/claude-settings-*.json >/dev/null 2>&1; then
    ok "a backup of settings.json is in Application Support/Farero/backups"
  else
    warn "no settings.json backup (expected when settings.json did not exist before)"
  fi
  if command -v claude >/dev/null; then
    r=$(cd /tmp && perl -e 'alarm 60; exec @ARGV' claude mcp get farero 2>&1)
    info "claude mcp get farero: $(echo "$r" | grep -iE 'status|timeout' | tr '\n' ' ')"
    echo "$r" | grep -q 'Connected' && ok "claude mcp get farero: connected" || fail "claude mcp get farero: connected"
  fi
}

# count <where>: number of calls rows matching the SQL condition.
count() { q "SELECT count(*) FROM calls WHERE $1"; }
expect() { # expect <message> <where>
  local n
  n=$(count "$2")
  [ "${n:-0}" -gt 0 ] && ok "$1 ($n)" || fail "$1"
}

step_log() {
  header "log: scenarios A-D in the audit log"
  [ -f "$DB" ] || { fail "database exists ($DB)"; return; }
  local bash_npm="kind='agent' AND tool='Bash' AND input_json LIKE '%npm install%'"
  local sid ts n
  info "all calls (time | session | kind | tool | decision | reason | error | input):"
  q "SELECT datetime(ts/1000,'unixepoch','localtime'), coalesce(substr(session_id,8,8),'unknown'), kind,
       CASE WHEN plugin='' THEN tool ELSE plugin||'_'||tool END, decision, reason,
       CASE WHEN error='' THEN '' ELSE 'error' END, substr(replace(input_json,char(10),' '),1,90)
     FROM calls ORDER BY ts" | sed 's/^/         /' | tee -a "$REPORT" 2>/dev/null

  out "  -- A: shell command approval"
  expect "A: Bash npm install allowed from the card" "$bash_npm AND decision='user_allowed'"
  expect "A: Bash npm install denied from the card" "$bash_npm AND decision='denied'"
  expect "A: Bash npm install ran on a session grant (no card)" "$bash_npm AND decision='session_allowed'"

  out "  -- B: Railway redeploy"
  expect "B: railway_redeploy allowed from the card and succeeded" "plugin='railway' AND tool='redeploy' AND decision='user_allowed' AND error=''"

  out "  -- C: taint"
  expect "C: github_issue_write allowed from the card" "plugin='github' AND tool='issue_write' AND decision='user_allowed'"
  sid=$(q "SELECT session_id FROM calls WHERE plugin='github' AND tool='issue_read' AND decision='auto_allowed' AND session_id IS NOT NULL ORDER BY ts LIMIT 1")
  if [ -z "$sid" ]; then
    fail "C: github_issue_read ran (auto) in a known session"
  else
    ts=$(q "SELECT min(ts) FROM calls WHERE session_id='$sid' AND plugin='github' AND tool='issue_read' AND decision='auto_allowed'")
    ok "C: github_issue_read ran (auto) in session ${sid:7:8}"
    n=$(q "SELECT tainted FROM sessions WHERE id='$sid'")
    [ "$n" = 1 ] && ok "C: that session is tainted" || fail "C: that session is tainted (tainted=$n)"
    expect "C: before the read, github_issue_write ran on a session grant in that session" "session_id='$sid' AND plugin='github' AND tool='issue_write' AND decision='session_allowed' AND ts<$ts"
    expect "C: after the read, github_issue_write asked again (tainted) and was denied" "session_id='$sid' AND plugin='github' AND tool='issue_write' AND decision='denied' AND reason LIKE '%tainted%' AND ts>$ts"
    # An agent tool answered from a card logs the answer, not why it asked,
    # so look for a command that had a session grant and was asked again.
    # Bash grants are scoped by command (Q38).
    expect "C: after the read, a Bash command granted for the session earlier was asked again" \
      "session_id='$sid' AND kind='agent' AND ts>$ts AND decision IN ('user_allowed','denied','timeout') AND EXISTS (
         SELECT 1 FROM calls g WHERE g.session_id=calls.session_id AND g.kind='agent' AND g.tool=calls.tool AND g.ts<$ts
           AND (g.reason='allow_session' OR g.decision='session_allowed')
           AND coalesce(json_extract(g.input_json,'\$.command'),g.input_json)=coalesce(json_extract(calls.input_json,'\$.command'),calls.input_json))"
    n=$(count "session_id='$sid' AND kind='agent' AND ts>$ts AND decision='session_allowed'")
    [ "${n:-0}" = 0 ] && ok "C: after the read, no agent tool ran on a session grant" || fail "C: after the read, no agent tool ran on a session grant ($n did)"
  fi

  out "  -- D: app not running"
  expect "D: github_get_me ran (auto)" "plugin='github' AND tool='get_me' AND decision='auto_allowed' AND error=''"
  expect "D: github_create_pull_request auto-denied because the app was not running" "plugin='github' AND tool='create_pull_request' AND decision='auto_denied' AND reason LIKE '%app_not_running%'"
  expect "D: an agent tool fell back to the terminal prompt" "kind='agent' AND decision='passthrough'"

  out "  -- other"
  n=$(count "session_id IS NULL")
  [ "${n:-0}" = 0 ] && ok "every call is tied to a session" || warn "$n call(s) with an unknown session"
  n=$(count "kind='plugin' AND error!=''")
  [ "${n:-0}" = 0 ] || info "$n plugin call(s) ended with an upstream error (see the table above)"
  secret_scan
}

# secret_scan: no token-shaped string in the log, the database, the config
# or the Claude Code files. Prints only where, never the match.
secret_scan() {
  local pat='gh[opsur]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|[Bb]earer [A-Za-z0-9._~+/=-]{20,}|ya29\.[A-Za-z0-9._-]{20,}|1//0[A-Za-z0-9._-]{30,}|re_[A-Za-z0-9]{8}_[A-Za-z0-9]{16,}|"(access|refresh)_token" *: *"[^"]{8,}'
  local f name n hits=0 tmp
  tmp=$(mktemp)
  sqlite3 -readonly "$DB" .dump > "$tmp" 2>/dev/null
  for f in "$tmp" "$LOGS/farerod.log" "$SUPPORT/config.json" "$SETTINGS" "$CLAUDE_JSON"; do
    [ -f "$f" ] || continue
    n=$(grep -E -c "$pat" "$f" 2>/dev/null)
    if [ "${n:-0}" -gt 0 ]; then
      hits=$((hits + n))
      name=$f; [ "$f" = "$tmp" ] && name="farero.db (sqlite3 .dump)"
      fail "no token in $name: $n line(s) look like one (lines: $(grep -E -n "$pat" "$f" | cut -d: -f1 | head -5 | tr '\n' ' '))"
    fi
  done
  rm -f "$tmp"
  [ "$hits" = 0 ] && ok "no token-shaped string in the database, farerod.log, config.json or the Claude Code files"
}

step_reboot() {
  header "reboot: farerod came back after a restart"
  local boot plist_t pid start
  boot=$(sysctl -n kern.boottime | sed -E 's/^\{ sec = ([0-9]+),.*/\1/')
  info "booted at $(fmt_time "$boot")"
  if [ -f "$PLIST" ]; then
    plist_t=$(stat -f %m "$PLIST")
    info "LaunchAgent plist written at $(fmt_time "$plist_t")"
    if [ "$boot" -gt "$plist_t" ]; then
      ok "the Mac restarted after the LaunchAgent was written"
    else
      fail "the Mac restarted after the LaunchAgent was written (restart, log in, and run this again)"
    fi
  else
    fail "LaunchAgent plist exists ($PLIST)"
  fi
  daemon_up
  pid=$(farerod_pid)
  if [ -n "$pid" ]; then
    start=$(start_epoch "$pid")
    info "farerod started at $(fmt_time "${start:-0}")"
  fi
  if ps -axo command= | grep -q "^$APP/Contents/MacOS/Farero$"; then
    info "Farero.app is running"
  else
    info "Farero.app is not running"
  fi
}

step_removed() {
  header "removed: after 설정 제거"
  claude_clean
  # The settings.json farero saw before its first install.
  local orig="$SUPPORT/backups/claude-settings-original.json" before tmp
  [ -f "$orig" ] || { info "no claude-settings-original.json in backups to compare with"; return; }
  before=$(js "$orig" 'j.existed ? j.content : "__ABSENT__"')
  if [ "$before" = "__ABSENT__" ]; then
    [ -f "$SETTINGS" ] && info "settings.json did not exist before farero and exists now" || info "settings.json did not exist before farero and is gone again"
  elif [ "$before" = "$(cat "$SETTINGS" 2>/dev/null)" ]; then
    info "settings.json is byte-identical to the one before farero"
  else
    info "settings.json differs from the one before farero (Claude Code also rewrites it on its own):"
    tmp=$(mktemp); printf '%s\n' "$before" > "$tmp"
    diff "$tmp" "$SETTINGS" | head -20 | sed 's/^/         /' | tee -a "$REPORT" 2>/dev/null
    rm -f "$tmp"
  fi
}

step_uninstalled() {
  header "uninstalled: after the README uninstall"
  no_trace
}

usage() { sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

touch "$REPORT" 2>/dev/null || { echo "cannot write the report $REPORT" >&2; REPORT=/dev/null; }
case "${1:-}" in
  pre) step_pre ;;
  download) step_download "${2:-}" ;;
  translocated) step_translocated ;;
  installed) step_installed ;;
  registered) step_registered ;;
  log) step_log ;;
  reboot) step_reboot ;;
  removed) step_removed ;;
  uninstalled) step_uninstalled ;;
  *) usage ;;
esac
if [ "$FAILS" -gt 0 ]; then
  out "  => $FAILS check(s) failed"
  exit 1
fi
out "  => all checks passed"
