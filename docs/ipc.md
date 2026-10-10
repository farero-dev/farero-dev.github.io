# farerod 로컬 IPC 규약

`farerod`와 클라이언트(`farero-hook`, 앱) 사이의 소켓 규약이다. Go 쪽 정의는 `daemon/internal/ipc/protocol.go`와 `daemon/internal/model/model.go`에 있고, 이 문서와 어긋나면 코드가 기준이다.

## 연결

- 소켓: `~/Library/Application Support/Farero/farerod.sock` (권한 0600). 개발할 때는 `FARERO_SOCKET` 환경 변수로 다른 경로를 쓴다.
- 형식: JSON Lines. 한 줄에 객체 하나. 각 객체는 다음 모양이다.

```json
{"type": "approval.request", "id": "선택", "data": { ... }}
```

- `id`: 요청을 보낼 때 붙이면 답장에 같은 `id`가 돌아온다. 서버가 먼저 보내는 알림(브로드캐스트)에는 `id`가 없다.
- 첫 메시지로 클라이언트 종류를 정한다: `hook.event`(훅), `headers.issue`(헤더 헬퍼), `ui.hello`(앱).
- 첫 메시지는 연결 후 5초 안에 와야 한다.
- 오류 답장은 `{"type":"error","id":"...","data":{"message":"..."}}`이다.
- 시각은 RFC 3339 문자열이다(Go `time.Time`). 값이 없는 시각 필드는 빠진다(`omitzero`).

## 공통 객체

### Session
```json
{
  "id": "claude:6fc2350b-...",        // "<agent>:<agent_session_id>"
  "agent": "claude",
  "agent_session_id": "6fc2350b-...",
  "cwd": "/Users/me/proj",
  "tty": "ttys019",                    // 터미널 점프용, 모르면 ""
  "pid": 25145,                        // 에이전트 프로세스
  "status": "running",                 // running | waiting_input | waiting_approval | ended | unknown
  "current_tool": "Bash",              // 없으면 ""
  "tainted": false,                    // 오염됨
  "started_at": "2026-10-02T09:41:37+09:00",
  "last_event_at": "2026-10-02T09:41:51+09:00"
}
```
표시 이름은 `cwd`의 폴더 이름, 에이전트 종류, 시작 시각을 묶어 만든다(기능 명세서 5-1).

`status`가 바뀌는 때(아키텍처 6장, Claude Code 2.1.290·2.1.293으로 확인):

| 상태 | 들어가는 때 |
|---|---|
| `running` | `UserPromptSubmit`, `PreToolUse`, `PostToolUse(Failure)`, 에이전트 도구 승인에 답한 뒤 |
| `waiting_input` | `SessionStart`(compact 제외), `Stop`, `StopFailure`, `Notification`(`idle_prompt`, MCP elicitation), `AskUserQuestion`, 사용자가 턴을 멈춤(Esc·Ctrl-C·터미널 프롬프트에서 거부. 훅 이벤트가 없어서 transcript의 중단 표시와 대기 중이던 훅의 종료로 알아낸다) |
| `waiting_approval` | `PermissionRequest`, `Notification`(`permission_prompt`). 그리고 그 세션의 승인 카드(게이트웨이·에이전트 도구)가 열려 있는 동안은 훅 이벤트와 관계없이 이 상태다. Claude Code는 120초 넘게 걸리는 MCP 호출을 백그라운드로 옮기며 카드가 열린 채 `PostToolUse`·`Stop`을 보내고, 카드가 열린 채 사용자가 입력할 수도 있다(M4, 2.1.293). 카드가 닫히면 훅 이벤트가 정한 상태로 돌아간다 |
| `ended` | `SessionEnd`. 종료된 세션은 `SessionStart`나 다른 에이전트 프로세스의 이벤트로만 다시 열린다 |
| `unknown` | 에이전트 프로세스가 5초 넘게 없음(`SessionEnd` 없이 끝난 경우), PID를 모르면 10분 동안 이벤트 없음 |

### Approval (승인 카드)
```json
{
  "id": "9f2c...",
  "kind": "plugin",                    // plugin(게이트웨이 호출) | agent(에이전트 자체 도구)
  "session_id": "claude:...",          // 세션 불명이면 ""
  "session_label": "proj",             // 세션 불명이면 "세션 불명"
  "agent": "claude",
  "plugin": "github",                  // agent 종류면 ""
  "tool": "issue_write",               // agent 종류면 "Bash", "Edit" 등
  "input": { ... },                    // 입력값 전체(JSON)
  "reasons": ["policy", "tainted"],    // 아래 표
  "allow_session": true,               // "이번 세션 동안 허용" 버튼을 보일지
  "created_at": "...",
  "deadline": "..."                    // 이 시각이 지나면 자동 거부(10분)
}
```

| reason | 카드 문구 예 |
|---|---|
| `policy` | 분류표에서 승인이 필요한 도구 |
| `tainted` | 이 세션이 신뢰할 수 없는 콘텐츠를 읽음(오염) |
| `unknown_session` | 어느 세션의 호출인지 알 수 없음 |
| `agent_request` | 에이전트가 자체 도구 권한을 요청함 |

### Call (감사 로그 한 줄)
```json
{
  "id": 42, "session_id": "claude:...", "conn_id": "a1b2...", "ts": "...",
  "kind": "plugin", "agent": "claude", "plugin": "github", "tool": "issue_write",
  "input": { ... }, "result_text": "잘린 결과", "result_bytes": 20480,
  "decision": "user_allowed", "reason": "allow_session", "duration_ms": 5321, "error": ""
}
```
`decision` 값은 다음과 같다.
- `auto_allowed`: 자동 허용
- `user_allowed`: 사용자가 허용
- `session_allowed`: 세션 허용 기록으로 실행
- `denied`: 사용자가 거부
- `auto_denied`: 자동 거부(앱 꺼짐)
- `timeout`: 승인 대기 시간 초과
- `blocked`: 정책으로 차단
- `passthrough`: 앱이 없어 에이전트의 원래 프롬프트로 넘김
- `cancelled`: 에이전트가 요청을 거둠. `reason`이 `answered_in_agent`면 farero가 답하기 전에 에이전트가 진행했다는 뜻이다(터미널 프롬프트에서 허용했거나 다른 훅이 답함). 비어 있으면 훅이 끝났다는 뜻이다(터미널에서 거부·Esc, 세션 종료)

에이전트 도구 줄(`kind: agent`)의 `denied`·`timeout`은 나중에 `cancelled`/`answered_in_agent`로 바뀔 수 있다. 사용자가 터미널에서 먼저 허용한 뒤 farero가 거부(또는 시간 초과)하면 Claude Code는 늦게 온 훅의 답을 무시하고 도구를 실행한다. 그 도구의 `PostToolUse`(같은 `tool_use_id`)가 오면 `farerod`가 그 줄을 고친다. 이때 알림은 따로 보내지 않는다

### PluginState
```json
{"plugin": "github", "status": "connected", "account_label": "octocat",
 "connected_at": "...", "error": "", "options": {"read_only": "false"}}
```
`status`는 `disconnected | connected | expired | error` 중 하나다.

## 훅 클라이언트 (`farero-hook --agent claude`)

| 방향 | type | data |
|---|---|---|
| → | `hook.event` | `{"agent":"claude","input":<훅 stdin JSON 원본>,"tty":"ttys019","pid":25145}` |
| ← | `hook.ack` | 없음. `PermissionRequest`가 아닌 이벤트의 답장 |
| ← | `hook.decision` | `{"behavior":"allow"\|"deny"\|"none","reason":"..."}`. `PermissionRequest`의 답장. 사용자가 답하거나 시간이 다 될 때까지 기다린다 |

`farero-hook`은 연결을 끊는 것으로 요청 취소를 알린다. 사용자가 터미널에서 먼저 답해 Claude Code가 훅을 끝낸 경우가 여기에 해당한다. 그러면 `farerod`는 카드를 거두고 `approval.cancelled`를 보낸다.

## 헤더 클라이언트 (`farero-hook --headers`)

| 방향 | type | data |
|---|---|---|
| → | `headers.issue` | `{"agent":"claude","pid":25145}` |
| ← | `headers.result` | `{"headers":{"Authorization":"Bearer …","X-Farero-Conn":"25145.…"}}` |

`pid`는 `farero-hook`의 부모 프로세스(claude)다. `X-Farero-Conn`은 `<pid>.<난수>`라서, 같은 인자의 호출이 여러 세션에 걸려 모호할 때 farerod가 재시작된 뒤에도 그 PID로 세션을 고를 수 있다. Claude Code는 이 헬퍼를 프로세스마다 한 번만 실행하고, farerod가 재시작돼도 다시 실행하지 않는다(M4).

## 앱 (UI 클라이언트)

연결하면 먼저 `ui.hello`를 보내고, `farerod`는 `state.snapshot`으로 답한다. 그 뒤로는 앱의 요청과 서버의 알림이 섞여서 온다. 연결된 앱이 하나도 없으면 다음과 같이 처리한다.
- 승인 단계의 게이트웨이 호출은 자동 거부한다.
- 에이전트 권한 요청은 `none`으로 답해 터미널 프롬프트로 넘긴다.

### 서버 → 앱 알림

| type | data | 뜻 |
|---|---|---|
| `state.snapshot` | `{"version","sessions":[Session],"approvals":[Approval],"plugins":[PluginState],"gateway":{"running","port","url","error"}}` | `ui.hello`의 답장. 앱은 이것으로 화면 상태를 처음부터 다시 만든다. `farerod`는 키체인을 읽기 전에 소켓을 열기 때문에, 게이트웨이 시작과 플러그인 복원이 끝나면 연결된 앱 모두에 요청 없이 다시 보낸다 |
| `session.updated` | Session | 세션 상태·현재 도구·오염 변경 |
| `session.removed` | `{"session_id"}` | 세션 삭제됨 |
| `approval.request` | Approval | 승인 카드 추가(도착 순서대로 쌓기) |
| `approval.cancelled` | `{"approval_id","reason":"timeout"\|"cancelled"\|"answered"}` | 카드 거두기. `answered`는 다른 앱(또는 같은 앱)이 이미 답했다는 뜻 |
| `call.logged` | Call | 감사 로그에 한 줄 추가됨(오류 캐릭터 상태 등에 사용) |
| `plugin.updated` | PluginState | 플러그인 연결 상태 변경 |
| `plugin.prompt` | `{"plugin","user_code","url","expires_at"}` | OAuth 진행 중 사용자가 할 일(device code 입력, 브라우저 열기) |
| `agentcfg.status` | AgentCfgStatus | 앱 위치가 바뀌어 `farerod`가 Claude Code 설정의 경로를 스스로 고쳤을 때(Q63). `message`에 알림 문구와 백업 경로가 있다. 같은 이름의 요청 답장과 모양이 같다 |

### 앱 → 서버 요청

| type | data | 답장 |
|---|---|---|
| `approval.response` | `{"approval_id","answer":"allow"\|"allow_session"\|"deny"}` | `ok` 또는 `error`(이미 끝난 카드) |
| `log.query` | `{"query","session_id","agent","plugin","tool","decision","kind","from","to","limit","offset"}` (모두 선택. `from`/`to`는 RFC 3339 문자열이고, 비우려면 빼거나 `""`) | `log.result`: `[Call]`. `query`는 3글자 이상이면 전문 검색, 2글자 이하면 부분 일치. `limit` 기본 200, 최대 1000 |
| `policy.get` | 없음 | `policy.state`: `[{"plugin","tool","level","no_session","taint","destructive","default_level","overridden"}]` |
| `policy.set` | `{"plugin","tool","level":"auto"\|"ask"\|"block"\|""}` | `policy.state`. `""`는 기본값으로 되돌림. `no_session` 도구를 `auto`로 바꾸려 하면 `error` |
| `session.delete` | `{"session_id"}` | `ok`. 모든 앱에 `session.removed`를 보냄 |
| `plugin.list` | 없음 | `plugin.list`: `[PluginState]` |
| `plugin.connect` | `{"plugin","params":{...}}` | `ok`가 바로 오고, 진행 상황은 `plugin.prompt`와 `plugin.updated`로 온다. Gmail은 `params`에 `client_id`, `client_secret`을 넣는다 |
| `plugin.disconnect` | `{"plugin"}` | `ok`. 토큰을 지운다 |
| `plugin.set_option` | `{"plugin","key","value"}` | `ok`. 예: GitHub `read_only` = `"true"` |
| `plugin.tools` | `{"plugin"}` | `plugin.tools`: `{"plugin","tools":[{"name","title","description","read_only_hint","destructive_hint","classified","level","exposed"}],"missing":[...]}`. 연결된 플러그인의 실제 `tools/list`를 분류표와 나란히 보여 준다. `classified: false`는 분류표에 없어 숨긴 도구, `missing`은 분류표에는 있지만 업스트림 목록에 없는 도구다(이름이 바뀌었거나 없어짐). 연결되지 않았으면 `error` |
| `settings.get` | 없음 | `settings.get`: `{"result_limit_bytes","update_check"}` |
| `settings.set` | `{"result_limit_bytes","update_check"}` | `settings.get` |
| `agentcfg.status` | `{"agent":"claude"}` | `agentcfg.status`: AgentCfgStatus |
| `agentcfg.plan` | `{"agent":"claude"}` | `agentcfg.plan`: `{"agent","changes":[{"path","before","after"}],"commands":[...]}`. 앱은 before/after diff를 보여 주고 확인을 받는다 |
| `agentcfg.apply` | `{"agent":"claude"}` | `agentcfg.status`(`backup_path` 포함). 백업한 뒤 적용한다 |
| `agentcfg.remove` | `{"agent":"claude"}` | `agentcfg.status`. farero가 넣은 항목만 지운다. 남은 내용이 처음 등록하기 전과 같으면 그때 파일을 바이트 그대로 되돌린다(farero가 만든 파일이면 지운다) |

AgentCfgStatus:
```json
{"agent":"claude","cli_found":true,"cli_path":"/Users/me/.local/bin/claude","version":"2.1.287",
 "min_version":"2.1.203","version_ok":true,"settings_path":"/Users/me/.claude/settings.json",
 "hooks_installed":true,"allow_installed":true,"mcp_installed":true,
 "hook_path":"/Applications/Farero.app/Contents/MacOS/farero-hook","stale_path":false,
 "gateway_url":"http://127.0.0.1:61511/mcp","backup_path":"...","message":"..."}
```
