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

### 앱 1단계 (`app/`, 서브 에이전트)
- SwiftPM 패키지: `FareroCore`(Foundation만: IPC 클라이언트, 모델, 상태 리듀서, 캐릭터 상태 결정, 노치 계산), `Farero`(AppKit/SwiftUI 메뉴바 앱), 테스트 77개 통과.
- IPC: POSIX 소켓을 전용 스레드에서 읽고, 줄 단위로 나누고(수 MB 줄 처리), 끊기면 1초마다 재연결하며 스냅샷으로 화면을 다시 만든다.
- 노치 패널: 포커스를 뺏지 않는 `NSPanel`(`.nonactivatingPanel`, `canBecomeKey=false`). 축소 상태는 노치 양옆에 22pt 캐릭터와 세션 수, 펼치면 세션 목록·현재 도구·터미널 버튼, 승인 카드는 도구·세션·이유·전체 입력·카운트다운·버튼 3개. 노치가 없는 화면은 상단 가운데.
- 단축키: Carbon `RegisterEventHotKey`(손쉬운 사용 권한 불필요). 승인 카드가 있을 때만 ⌃⌥Y 허용 / ⌃⌥S 세션 허용 / ⌃⌥N 거부.
- 캐릭터 상태 결정: 연결 끊김 > 승인 필요 > 허용함(1.5초) > 거부함(1.5초) > 오류(3초) > 조심 > 여럿 작업 중 > 작업 중 > 생각 중 > 인사(2초) > 입력 대기 > 잠듦. 터미널 프롬프트로 넘어간 `waiting_approval` 세션은 "입력 대기"로 보이게 고쳤다.
- 실제 데몬으로 확인: 연결·스냅샷, 캐릭터 전환, 축소·확장·승인 카드 화면, 거부 시 게이트웨이 오류 문구, 훅 취소 시 카드 제거, 데몬 재시작 후 1초 안에 재연결.
- 확인 못 한 것: 단축키 실제 키 입력(손쉬운 사용 권한 없음), Terminal.app 점프, SMAppService 등록(번들 필요), 메뉴 창들(포커스를 뺏어서 사용자 작업 방해 방지).
- 명세와 다른 점: 7.5MB 같은 큰 입력은 카드에 앞 256KB만 보이고 "전체 복사" 버튼을 둔다(메모리 1.4GB → 144MB). 입력 JSON은 키를 정렬해 보여 준다.

### 앱 제안으로 바꾼 데몬
- 한 UI가 승인에 답하면 `approval.cancelled`(reason `answered`)를 모든 UI에 보낸다.
- 스냅샷 `plugins`가 `null`이 되지 않게 했다. 쓰지 않는 `gateway.status` 상수를 지웠다.
- `log.query`의 `from`/`to`가 `""`나 `null`이어도 받는다.

### 배포 빌드 (`scripts/build.sh`)
- `scripts/build.sh 0.1.0-dev` 첫 실행 성공(31.6초, `JOBS=2`). Farero·farerod·farero-hook 모두 `x86_64 arm64`, ad-hoc 서명 검증 통과(`--strict`), Info.plist 버전 치환, LaunchAgent plist(`BundleProgram Contents/MacOS/farerod`), x86_64 쪽도 Rosetta로 실행 확인, zip 약 15MB, farerod는 시스템 프레임워크만 링크.
- GitHub Actions: `ci.yml`(develop 푸시 시 Go·Swift 테스트), `release.yml`(`v*` 태그 → Universal 빌드 → Releases). GitHub OAuth App client ID는 저장소 변수 `FARERO_GITHUB_CLIENT_ID`로 넣는다.
- farerod는 LaunchAgent로 실행될 때 `~/Library/Logs/Farero/farerod.log`에 로그를 남긴다(10MB 넘으면 새로 시작).

### 앱 2단계 (서브 에이전트)
- 설정 창(승인 정책·일반·Claude Code 등록과 diff 확인, 다른 앱 PermissionRequest 훅 경고), 플러그인 창(device code 표시, Gmail GCP 안내 시트, GitHub 읽기 전용), 로그 검색(필터·상세·세션 삭제), 노치 세션 삭제, 설정 도우미, 업데이트 확인(GitHub Releases, 404는 정상), 앱·데몬 버전 불일치 시 데몬 재등록. 테스트 111개 통과.
- 남은 데몬 수정: (1) 처음 연결 실패 이유가 `plugin.updated`에 실리지 않음, (2) 미연결 상태 `plugin.set_option`이 재연결 실패로 오류, (3) `call.logged` 알림에 큰 입력이 그대로 실림, (4) `claude mcp add-json`이 적용 직후 settings.json을 다시 고치는 점 안내.

### CI 수정과 데몬 마무리
- CI 실패 원인: `macos-15` 러너의 이전 Xcode(Swift)에서 `SMAppService`가 Sendable이 아니라서 `try await service.unregister()`가 동시성 오류(로컬 Swift 6.3에서는 안 남). 비격리 정적 함수 안에서 서비스 객체를 만들어 해제하도록 고쳤다. 워크플로에 툴체인 버전 출력 단계를 추가했다.
- 데몬: 처음 연결 실패 이유를 `plugin.updated`의 `error`로 전달(상태 `error`), 미연결 플러그인의 `plugin.set_option`은 저장·알림만 하고 재연결하지 않음, `call.logged` 알림은 입력 8KB·결과 2KB로 줄임(저장된 로그는 전체), Claude CLI가 등록 직후 settings.json을 다시 쓰면 상태 메시지로 알림. 테스트 추가.

### 반복 테스트 (지침: 구현 후 루프, 최대 5회)
`scripts/e2e.sh`: 개발 데몬 + 격리된 Claude 설정 폴더에 실제 등록(`agentcfg apply`) → 그 결과물(settings.json, MCP 항목) 그대로 실제 `claude -p`(haiku) 실행 → 감사 로그로 판정. 사용자 `~/.claude`는 건드리지 않는다.

| 회차 | 조건 | 결과 | 조치 |
|---|---|---|---|
| 1 | Go `-race` + Swift 2종 + E2E 기본 | 단위 테스트 통과, E2E 실패 | 스크립트 버그: `timeout`이 셸 함수를 실행 못 해 claude가 안 돌았음. 호출 0건에도 통과하는 판정 2개 발견 → 수정 |
| 2 | 같은 조건 재실행 | 기능 판정 14개 통과, 1개 실패 | 실패한 판정은 모델의 최종 요약에 거부 사유가 있는지 보는 불안정한 판정 → `stream-json`의 도구 결과(`is_error` + 사유)로 확인하게 변경 |
| 3 | + 병렬 세션 두 개가 같은 인자로 동시 호출(모호한 경우 강제) | **19개 전부 통과** | 6건이 0.093초 간격까지 겹쳤는데도 모두 제 세션에 연결(연결 2개 ↔ 세션 2개 1:1). 종료 |

확인한 시나리오: A(셸 명령을 farero에서 허용 → 실행), C(오염 뒤 세션 허용 무효), D(앱 꺼짐 → 승인 도구 자동 거부, 조회 도구는 동작, 모델이 `is_error`와 사유를 받음), 세션 허용, 세션 연결, 설정 등록·제거.
CI(GitHub Actions, macOS 15.7 / Xcode 16.4 / Swift 6.1.2)도 통과.

## 2026-10-05 ~ 10-06

### main 병합
- develop → main PR #1을 merge commit(`2fc837c`)으로 병합했다. 로컬 Go `-race`·Swift 테스트·Universal 빌드와 PR CI가 통과한 뒤 병합했다. Pages(legacy)가 다시 빌드돼 Resend CIMD 문서 `https://farero-dev.github.io/oauth/client-metadata.json`이 200(`application/json`)으로 열린다.

### M0 남은 실험 (Claude Code 2.1.289, macOS 26.6.2)

**5. 터미널을 닫았을 때 `SessionEnd`: 통과.** 기록용 훅(`experiments/e1-correlate/hooklog`)과 격리 설정(`--settings`, `--setting-sources project`)으로 실행했다. 이 세션의 환경 변수(`CLAUDE_CODE_CHILD_SESSION` 등)는 지우고 실행했다.

| 방식 | 경우 | SessionEnd | claude 종료 |
|---|---|---|---|
| pty를 닫음(창을 닫을 때와 같은 SIGHUP) | 입력 대기 | +0.03초, reason `other` | 1.15초, 종료 코드 129 |
| 〃 | 도구 실행 중(`ping -c 90`) | +0.05초 | 0.93초, ping도 종료 |
| 〃 | `PermissionRequest` 훅 대기 중 | +0.14초 | 0.94초, 대기하던 훅 프로세스도 종료 |
| 실제 Terminal.app 창 닫기 | 입력 대기 | claude 종료 0.74초 전 | 정상 |

- Terminal.app은 실행 중인 프로세스가 있으면 AppleScript `close`에도 종료 확인 시트를 띄운다. 사용자가 "종료"를 눌러야 닫힌다.
- 대기 중인 훅 프로세스가 함께 끝나므로, `farero-hook`의 소켓이 끊기면 승인 카드를 거두는 현재 방식으로 충분하다. "10분 무응답 + 프로세스 없음 → 알 수 없음"은 강제 종료(kill -9) 같은 경우의 안전망으로 남긴다.
- 참고: Claude Code가 단독 `sleep 90` 명령을 자체 정책으로 막는다.

**3. SMAppService 등록(ad-hoc 서명): 등록은 통과, 업데이트는 실패 → 수정.**
- 깨끗한 상태(설치·데이터·launchd 항목 없음)에서 `/Applications/Farero.app`을 처음 열자 `requiresApproval` 없이 바로 `enabled`로 등록됐다(BTM `enabled, allowed, notified`). farerod가 실행돼 소켓(0600)과 게이트웨이를 열고, 앱이 소켓에 연결됐다. M1 완료 기준 중 "앱을 열면 데몬이 등록·실행"이 확인됐다.
- **앱 번들을 새 빌드로 바꾸면 farerod가 다시 뜨지 않는다.** BTM이 기존 항목을 무효화하고(`Bundle identifiers from launchd plist ignored because the executable doesn't have a Team ID`) 새 항목을 만드는데, launchd 작업은 예전 BTM 항목을 가리킨 채 `needs LWCR update` 상태가 된다. 그래서 `Could not find and/or execute program … Contents/MacOS/farerod`로 10초마다 실패한다(EX_CONFIG). 앱은 데몬이 떠서 버전을 알려 줘야만 다시 등록했기 때문에 복구하지 못했다.
- 다시 등록(unregister → register)해도 풀리지 않는다. BTM이 같은 라벨의 기존 항목과 그 LWCR(이전 빌드의 cdhash)을 재사용해서(`registerLaunchItem: found existing item`), 새 farerod는 `Launch Constraint Violation (Constraint not matched)`, `OS_REASON_CODESIGNING`으로 거부된다. launchd의 LWCR 복구 요청도 smd가 `EINVAL`로 거부한다. Team ID가 없는 ad-hoc 서명에서는 SMAppService 에이전트가 업데이트를 견디지 못한다.
- **결정(사용자, Q55 변경): SMAppService 대신 `~/Library/LaunchAgents`에 plist를 두고 `launchctl`로 등록한다.** 실험(`experiments/e5-launchagent`): ad-hoc 프로브 v1을 LaunchAgent로 띄운 뒤 번들 디렉터리를 v2로 통째로 바꾸고 프로세스를 끝내자 KeepAlive가 v2를 띄웠다. `launchctl kickstart -k`로도 즉시 새 바이너리가 떴다. BTM에는 plist 경로 기준 항목(`enabled, allowed, notified`)으로 기록되고 제약 위반 로그는 없다.
- 앱 구현: 시작할 때 이전 빌드의 SMAppService 등록을 지우고(같은 라벨이라 먼저 지워야 함), plist가 없거나 다른 farerod 경로를 가리키면 쓰고 `bootout`/`bootstrap`한다. 경로가 같고 farerod의 cdhash가 지난번과 다르면(업데이트·재빌드) `kickstart -k`로 새 바이너리를 띄운다(`LaunchAgent`, `DaemonRegistrationCheck`). 데몬 버전이 앱과 다를 때도 `kickstart -k`한다.
- 덧붙여, 노치 등대가 움직이지 않았던 것은 이 문제로 farerod가 뜨지 않아 앱이 "연결 끊김"(정지 상태)이었기 때문이다. 같은 설정의 패널을 재현해 보니 `TimelineView`는 accessory 앱의 non-activating 패널에서도 `cadence == .live`로 정상 동작했다.

**앱 번들 실제 조작:** 격리 설정의 claude를 Terminal.app에서 띄워 `PermissionRequest` 두 건을 만들었다. 두 건 모두 farero로 처리됐다(`user_allowed`, `user_allowed`/`allow_session`). 단축키와 클릭 중 어느 쪽으로 눌렀는지, 터미널 점프, 메뉴의 "데몬 연결됨" 표시는 확인하지 못했다.
- 확인한 사실: 승인 대기 중에 Claude Code의 `Notification`(권한 알림)이 오면 세션 상태가 `waiting_approval`에서 `waiting_input`으로 덮어써진다(`session.go`). 승인 카드는 broker가 관리하므로 계속 보인다. M2 상태 기계에서 다룬다.

**4. OAuth 실제 연결**
- **Railway: device flow 불가 → 인가 코드 + PKCE + loopback으로 변경(Q49 변경).** 메타데이터(`grant_types_supported`)에는 device_code가 있고 DCR도 그 grant로 등록해 주지만, device/auth는 `device_code is not allowed for this client`로 거부한다. redirect_uri 없이 등록한 클라이언트는 `redirect_uris must contain members`로 거부한다. 반면 native 클라이언트(`http://127.0.0.1/callback` 등록)는 포트가 다른 loopback 주소(`:54321/callback`)도 받아 준다(303 → 로그인). 등록되지 않은 주소는 400 `invalid_redirect_uri`다. 이전 빌드가 저장한 device flow용 setup(auth_url 없음)은 버리고 다시 등록한다.
- Railway 메타데이터에 이제 `client_id_metadata_document_supported: true`가 있다(검증 결과 문서에는 CIMD 미지원으로 적혀 있음).
- **Resend: 네트워크 문제로 보류.** 브라우저가 `resend.com`(Vercel 76.76.21.22)에 연결하지 못했다(ERR_CONNECTION_TIMED_OUT). 이 네트워크(en0 → 192.168.1.1)에서 76.76.21.22:443만 연결되지 않고, 76.76.21.21:443과 `api.resend.com`(Cloudflare)은 된다. farero 코드와는 무관하다.
- **GitHub: 통과.** `farero-dev` 조직에 OAuth App `farero`를 만들었다(Client ID `Ov23ctQpWIVLVb8jUWxb`, device flow 켬, 만료형 사용자 토큰 켬, callback `http://127.0.0.1/callback`). 저장소 변수 `FARERO_GITHUB_CLIENT_ID`에도 넣었다. device flow 토큰으로 원격 MCP가 동작한다: 게이트웨이를 거친 `github_get_me` → `seongj-un`(439ms, `auto_allowed`). 만료형 토큰의 자동 갱신은 8시간 뒤 확인이 필요하다.
- **Railway: 통과(loopback).** DCR + 인가 코드 + PKCE + loopback으로 연결했다. 게이트웨이를 거친 `railway_whoami` → `seongj-un`(171ms, `auto_allowed`). 두 호출 모두 `PreToolUse`로 같은 세션에 연결됐다.
- **Resend: 보류.** 네트워크 문제(76.76.21.22)가 그대로라 연결하지 못했다.
- **보류한 항목(사용자 결정, 2026-10-06):** Resend 연결, Gmail(사용자 GCP 클라이언트 필요), 재부팅 뒤 데몬 유지, 단축키·터미널 점프 실제 조작. M1 작업을 먼저 진행한다.
- 확인한 사실: farerod는 키체인 응답을 받기 전에는 소켓을 열지 않는다. ad-hoc 빌드를 업데이트할 때마다 키체인 확인 창이 뜨는데, 사용자가 답하기 전까지 앱은 "연결 끊김"이다.
