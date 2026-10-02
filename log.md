# farero 작업 로그

구현 진행 기록. 새 항목을 아래에 덧붙인다. 설계 문서는 Notion(CLAUDE.md의 링크)에 있고, 여기에는 실제로 한 일과 그 과정에서 확인한 사실, 내린 판단을 남긴다.

## 2026-10-02

### 준비
- Notion 문서 5개(프로젝트 페이지, 기능 명세서 v0.3, 아키텍처 상세 설계 v0.2, 구현 전 검증 결과, MVP 구현 순서 v0.2)를 읽고 구현을 시작했다.
- Homebrew로 Go 1.27.1을 설치했다(`modernc.org/sqlite`는 Go 1.26 이상 필요).
- 저장소: `farero-dev/farero-dev.github.io`를 `origin`으로 연결했다. 작업은 `develop` 브랜치에 올리고, main 병합은 나중에 따로 한다(사용자 지시). main은 GitHub Pages(루트, legacy Jekyll)로 배포된다.
- `CLAUDE.md`를 작성했다(`/init`).

### M0 실험 (맥에서 실행, 코드는 `experiments/`)
실험은 사용자 전역 설정을 건드리지 않도록 `claude -p --mcp-config/--settings/--strict-mcp-config`로 돌렸다. Claude Code 2.1.287 기준.

| # | 항목 | 결과 |
|---|---|---|
| 1 | `PreToolUse`와 MCP 호출 도착 순서 | **통과.** `PreToolUse`가 MCP `tools/call`보다 60~90ms 먼저 도착 |
| 1 | `tool_input`과 MCP 인자 일치 | **정규화하면 통과.** 값은 같지만 키 순서가 다름(훅은 모델이 쓴 순서, MCP는 키 정렬). 배열 순서는 유지. → 정렬한 JSON 해시로 비교 |
| 1 | 병렬 호출 | 한 메시지에 두 호출을 요청해도 이번 실행에서는 순서대로(Pre A → call A → Post A → Pre B …) 처리됨. 매칭은 키 기준이라 영향 없음 |
| 2 | `headersHelper` | `--mcp-config`에서 동작. 연결할 때 1회 실행. 받는 환경 변수는 `CLAUDE_CODE_MCP_SERVER_NAME/URL`, `CLAUDE_CODE_ENTRYPOINT` 등이고 **세션 ID는 없음**. 단, helper와 훅 프로세스의 부모 PID가 둘 다 claude 프로세스로 같음 → 연결↔에이전트 PID 대응에 쓸 수 있음 |
| 2 | 서버별 `timeout: 660000` | **필요하고 동작함.** 75초 도구 호출이 `timeout` 있으면 성공, 없으면 약 60초에 "The operation timed out"으로 실패 |
| 2 | 접속 프로토콜 | 먼저 `server/discover`(2026-07-28)를 보내고, Go SDK 서버가 2026-07-28을 지원 목록에 넣지 않자 `initialize`(2025-11-25)로 접속 |
| 2 | allow 규칙과 `PermissionRequest` | `permissions.allow`의 `mcp__farero__*` 도구는 `PermissionRequest`가 불리지 않음. 허용 규칙이 없는 Bash는 `PreToolUse` 뒤 약 80ms에 `PermissionRequest` 발생, 훅의 `decision.behavior: allow` 응답으로 실행됨 |
| 2 | user 스코프 등록 | `claude mcp add-json farero '<json>' --scope user`가 `headersHelper`와 `timeout`을 그대로 기록함(`CLAUDE_CONFIG_DIR` 임시 폴더로 확인). Q54의 "설정 파일 직접 기록" 대신 CLI를 쓸 수 있음 |
| 2 | 훅 대기 중 터미널 | 훅이 기다리는 동안 터미널에는 Claude Code 자체 권한 프롬프트가 함께 떠 있음 → 앱이 없어도 터미널에서 답할 수 있음 |
| 2 | 훅 `timeout: 660`으로 10분 대기 | **통과.** 대화형 세션(pty)에서 `PermissionRequest` 훅이 620초(기본 600초 초과) 기다린 뒤 allow → "Allowed by PermissionRequest hook"으로 실행됨. 사용자 설정의 다른 훅을 빼려고 `--setting-sources project`로 실행 |
| 2 | `-p` 모드의 권한 요청 | 비대화형(`-p`)에서는 훅이 늦게 답하면 몇 초 만에 자동 거부됨. 10분 대기는 대화형에서만 확인 가능 |
| 2 | 다른 앱의 훅 | 사용자 전역 설정에 다른 앱의 `PermissionRequest` HTTP 훅(`127.0.0.1:23333`)이 있었고, 그 훅의 거부가 farero 훅보다 먼저 적용됨 → 온보딩에서 안내 필요 |
| 3 | `Terminal.app` 탭의 tty | `sdef`에 `tab` 클래스의 `tty` 속성(읽기 전용)이 있음 |
| 3 | tty 조회 | 훅 프로세스의 부모(claude)에 `ps -o tty=`로 tty를 얻음(`ttys019`) |
| 3 | go-keyring | **기준 미달.** go-keyring으로 만든 항목을 `/usr/bin/security find-generic-password -w`가 확인 창 없이 읽음 → Q56대로 Security 프레임워크(cgo)로 교체 |
| 5 | `SessionEnd` | `-p` 종료 시 reason `other`로 발생. 터미널을 닫는 경우는 아직 미확인 |

### 실험 결과로 바꾼 구현 판단
- 세션 연결: 기본은 설계대로 `PreToolUse` 매칭(30초, 정규화 키). 같은 키가 여러 세션에 걸려 모호할 때만 연결의 에이전트 PID로 고른다. `PreToolUse` 없이 온 호출은 PID로 추정하지 않고 세션 불명으로 둔다(엄격한 쪽).
- Keychain은 cgo로 Security 프레임워크를 직접 쓰고, Keychain은 `farerod`만 만진다. 훅의 헤더 헬퍼는 소켓(`headers.issue`)으로 비밀값을 받는다.

### 구현 (daemon/)
- `store`: SQLite 스키마 v1(`PRAGMA user_version` 마이그레이션, 마이그레이션 전 백업), 세션·훅 이벤트·감사 로그·정책 재정의·플러그인·설정. `calls_fts`는 FTS5 trigram, 2글자 이하는 LIKE. 세션 삭제 시 로그·검색 인덱스까지 삭제.
- `policy`: 기본 분류표 `policy/default.json`(GitHub·Railway·Resend·Gmail, 검증 결과 5장 기준), 판단 순서, 세션 허용(메모리), 오염 시 무효, `no_session` 도구는 자동 허용으로 못 바꿈.
- `correlate`: 위 세션 연결.
- `broker`: 승인 대기열(도착 순서), 10분 마감, 앱 연결이 모두 끊기면 대기 중 요청을 "UI 없음"으로 해제, 에이전트 취소 처리.
- `session`: 훅 이벤트 상태 기계, 처음 보는 세션 즉석 생성, 오염 영구 저장, 10분 무응답 + 프로세스 없음 → 알 수 없음.
- `ipc`: JSON Lines 코덱, 0600 Unix 소켓, 다른 farerod가 살아 있으면 시작 거부.
- `gateway`: Go MCP SDK Streamable HTTP, 127.0.0.1, `Origin` 헤더 403, Bearer 인증, 도구 목록 차분 갱신, 잘못된 업스트림 스키마는 건너뜀.
- `upstream`: 플러그인 인터페이스, 개발용 가짜 플러그인(`devplugin`, `farero_dev` 빌드 태그로만 포함 예정).
- `secret`: Keychain(cgo) / 개발용 파일 저장소 / 테스트용 메모리 저장소.
- 모든 패키지 테스트 통과(`go test -race`).

### 실행 파일과 첫 통합 실행
- `farero-hook`(훅·헤더 헬퍼), `farerod`(데몬), `farero-devctl`(앱 대신 쓰는 개발용 UI 클라이언트)을 만들었다. 가짜 플러그인은 `-tags farero_dev` 빌드에만 들어간다.
- `core` 통합 테스트 14개: 실제 소켓·게이트웨이·MCP 클라이언트로 M4 완료 기준(자동 허용 / 승인 / 세션 허용 / 세션 허용 불가 / 오염 / 세션 불명 / 앱 꺼짐 자동 거부 / 차단 도구 숨김 / 시간 초과 / 에이전트 취소 / 정책 재정의)을 확인. `-race -count=5` 통과.
- **실제 Claude Code 2.1.287로 통합 실행**(`claude -p`, `--setting-sources project`, 사용자 설정과 분리): 9개 호출이 모두 세션에 연결됨(세션 불명 0건). 기록된 판단은 순서대로 auto_allowed → user_allowed(allow_session) → session_allowed → auto_allowed(오염 소스) → user_allowed(policy,tainted: 세션 허용 무효, 버튼 숨김) → user_allowed(destroy, 세션 허용 불가) → Bash 2회 모두 다시 물음(오염 세션, Q39) → 업스트림 실패 기록.

### 에이전트 설정 자동 등록 (F-06)
- `agentcfg`: Claude Code 사용자 `settings.json`에 훅 9종(`timeout: 660`)과 `permissions.allow`의 `mcp__farero__*`를 넣고, 게이트웨이는 `claude mcp add-json farero … --scope user`(서버별 `timeout: 660000`, `headersHelper`)로 등록한다. 키 순서를 보존하고, `&&` 같은 문자를 이스케이프하지 않으며(`\u0026` 방지), 쓰기 전에 `backups/`에 원본을 저장한다. 제거는 farero 항목만 지운다. 앱 위치가 바뀌면 시작할 때 경로만 고치고 알림 문구를 남긴다(Q63).
- `farerod`에 `agentcfg.status/plan/apply/remove` 요청을 연결했다. `FARERO_CLAUDE_CONFIG_DIR`로 대상 설정 폴더를 바꿀 수 있고, `--dev`에서 이 값이 없으면 경로 자동 수정을 하지 않는다(실제 `~/.claude` 보호).
- 실제 `claude` CLI(임시 `CLAUDE_CONFIG_DIR`)로 확인: 등록 후 `claude mcp get farero`가 "✔ Connected, Timeout 660000ms"(헤더 헬퍼 인증 통과), 제거 후 farero 항목만 빠짐.
- 확인한 사실: `claude` CLI(`mcp add-json`/`mcp get`)가 실행 중에 `settings.json`을 스스로 다시 쓴다(키 순서 변경, `model: "opus"` → `"opus[1m]"`). 그래서 계획(diff)과 적용 사이에 파일이 바뀔 수 있고, 적용은 그 시점의 파일을 다시 읽어 처리한다.
- `docs/ipc.md`: 소켓 규약 문서.

### 작업 방식 메모
- CPU 사용량이 커서(서브 에이전트 두 개의 `swift build`와 `go build`가 동시에 돌았음) 작업을 멈췄다가, 무거운 작업은 하나씩 순서대로 진행하기로 했다. 빌드 병렬도도 제한한다(`go build -p 2`, `swift build -j 2`).

### 캐릭터: 등대 v2 (F-12, `app/LighthouseKit`)
- 서브 에이전트가 SwiftUI 도형으로 그렸다. 22단위 격자(22pt에서 1단위 = 1pt), 짧고 넓은 탑, 눈 지름 2.2pt, 선 굵기 22pt에서 1pt. 색은 4개(몸 `#F7F4EC`, 구조 회색 `#98A1B3`, 잉크 `#1C2233`, 램프색)이고 켜진 유리창은 램프색을 섞은 불투명 단색이다. 반투명·그라디언트·원형 배지 없음.
- 상태 12종마다 불빛(F/Q/Fl/Oc/Iso/Al/회전 빛줄기/꺼짐), 눈, 배지, 몸짓이 함께 달라진다(색 말고도 두 가지 이상 차이). 가장 빠른 점멸은 1초에 1번(Q, 0.4초 켜짐). "동작 줄이기"나 `animated: false`면 깜빡임·회전·점프를 멈추고 불은 켠 채로 둔다. 바뀔 때만 다시 그린다.
- 테스트 15개: 1ms 간격으로 30초 샘플링해 1Hz 초과 점멸 없음, 동작 줄이기 시 고정, 꺼짐 상태 3종은 항상 꺼짐, 상태끼리 색 외 단서 2개 이상 차이 등. 기준을 일부러 어겨 테스트가 실패하는지도 확인했다.
- 앱 쪽 참고: 뷰 폭은 `size × 24/22`(오른쪽에 배지·빛줄기 자리). `LighthouseView.aspectRatio`로 노출.

### M5 플러그인 코드 (`auth`, `upstream`, `plugins`)
- `auth`: 메타데이터 조회(RFC 8414/9728, https만), device flow(RFC 8628), loopback + PKCE(S256, state 검증), `resource` 파라미터를 붙이는 토큰 갱신(x/oauth2 기본 갱신은 못 붙임), 갱신으로 바뀐 토큰(Resend는 refresh token이 매번 바뀜)을 자동 저장, 갱신 실패 시 "토큰 만료" 처리.
- `upstream.Remote`: 원격 Streamable HTTP MCP 클라이언트. 처음 쓸 때 연결, 실패하면 다음에 재연결. 호출 자체는 재시도하지 않음(5-4).
- `upstream/gmail`: Gmail REST API를 직접 호출하는 자체 도구 4개(검색·메일·스레드·라벨). `google.golang.org/api`는 무거워서 쓰지 않았다. text/plain 우선, 없으면 HTML에서 텍스트 추출, 본문 32KB 제한. 검색 결과에도 외부인이 쓴 제목·발췌가 있어 `search_messages`도 오염 소스로 분류했다.
- `plugins`: GitHub(device flow, `repo read:org`, `X-MCP-Toolsets`, 읽기 전용 스위치 `X-MCP-Read-Only`), Railway(메타데이터 조회 → DCR → device flow, `resource` 포함), Resend(CIMD client_id `https://farero-dev.github.io/oauth/client-metadata.json`, loopback `/callback`, `full_access`), Gmail(사용자 GCP 클라이언트, `access_type=offline`, `prompt=consent`). 데몬 재시작 시 저장된 토큰으로 복원.
- `oauth/client-metadata.json`: Resend CIMD 문서. **GitHub Pages는 main에서 배포되므로 main에 병합해야 실제 주소에서 열린다.**
- 실제 메타데이터 확인(curl): Railway는 `mcp.railway.com` → `backboard.railway.com`(DCR `/oauth/register`, device `/oauth/device/auth`, S256). Resend는 리소스 식별자가 `https://mcp.resend.com`(루트)이고 MCP 엔드포인트는 `/mcp`라서 둘을 분리했다(버그 수정). `client_id_metadata_document_supported: true`. GitHub MCP의 리소스는 `https://api.githubcopilot.com/mcp`, 인증 서버 `https://github.com/login/oauth`.
- 아직 못 한 것: 실제 계정으로 연결(M0-4). GitHub OAuth App(`farero-dev` 조직, device flow 켜기)의 client ID가 필요하다(`-X main.githubClientID=…` 또는 `FARERO_GITHUB_CLIENT_ID`).
- 테스트: 가짜 OAuth 서버로 loopback(PKCE·resource·state 위조 거부), device flow(authorization_pending 후 성공), 갱신(refresh token 교체·저장, 실패 시 만료 콜백 1회), 가짜 Gmail API, SDK로 띄운 가짜 MCP 서버로 원격 플러그인.
