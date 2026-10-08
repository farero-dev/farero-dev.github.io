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

### M1 마무리 (2026-10-06)
M1 코드(모노레포, farerod 소켓·SQLite, farero-hook, 메뉴바 앱, 빌드 스크립트)는 develop에 이미 있었다. 완료 기준을 다시 확인하면서 M0에서 드러난 빈틈을 메웠다.
- **데몬 등록(Q55 변경):** 위의 LaunchAgent 전환. `KeepAlive`는 `PathState`(farerod 경로가 있을 때만)로 둔다. 프로브로 확인한 동작: 파일이 있으면 프로세스가 죽어도 다시 뜨고, 앱을 지우면 재시도를 멈추고(10초마다 실패하는 로그가 생기지 않음), 다시 설치하면 launchd가 알아서 띄운다. 예전 plist(`KeepAlive=true`)는 앱이 다를 때 다시 써서 바뀐다.
- **소켓을 키체인보다 먼저 연다:** ad-hoc 업데이트 뒤 키체인 창에 답하기 전까지 데몬이 통째로 멈추던 문제다. 게이트웨이 비밀값은 `core.New`가 아니라 `StartGateway`에서 읽고, 그 전에 나온 도구 목록은 보관했다가 게이트웨이를 만들 때 넣는다. farerod는 처리기 등록 → 소켓 열기 → 플러그인 복원·게이트웨이 시작(키체인) → 연결된 앱에 `state.snapshot` 재전송 순서로 시작한다. 경로 자동 수정(Q63)은 게이트웨이가 뜬 뒤 한다. 게이트웨이가 아직 없을 때 Claude Code 등록을 누르면 키체인 대기 중일 수 있다고 알린다. 테스트: 응답하지 않는 키체인을 흉내 낸 저장소로도 `core.New`가 바로 끝나고 앱이 연결되며, 게이트웨이가 뜨면 스냅샷을 다시 받는다.
- **완료 기준 테스트:** 데몬이 없을 때(소켓 없음, 죽은 데몬의 소켓 파일만 남음) farero-hook이 1초 안에 exit 0으로 끝나고 결정을 출력하지 않는다. 헤더 헬퍼 모드는 `{}`를 출력한다(`cmd/farero-hook/main_test.go`).
- e2e 스크립트는 소켓이 아니라 "gateway listening" 로그를 기다리게 바꿨다(소켓이 먼저 열리므로).

### M2 세션 감시 (2026-10-06)
M2 코드(훅 → 세션 상태 기계 → 앱 목록, `sessions`·`hook_events`, F-06 훅 등록)는 대부분 이미 있었다. 완료 기준을 확인하면서 실제 Claude Code의 훅 동작을 다시 재 보고, 그 결과로 상태 기계와 등록을 고쳤다. 브랜치는 develop(M1 마무리) 위에서 시작했다.

**M2 실험 (Claude Code 2.1.290, pty로 대화형 실행, `--setting-sources project`).** 하네스와 원본은 `build/m2exp`(커밋 안 함).

| 경우 | 오는 훅 이벤트 |
|---|---|
| 터미널 권한 프롬프트에서 No·Esc | **없음.** 기다리던 `PermissionRequest` 훅이 약 0.6초 뒤 SIGTERM을 받는다. transcript에 `[Request interrupted by user for tool use]`가 남는다 |
| 터미널 권한 프롬프트에서 Yes | 훅은 끝나지 않고 계속 기다린다. 도구가 돌고 `PostToolUse`가 온다(M0 기록의 "터미널에서 먼저 답하면 훅이 끝난다"는 거부·Esc에만 맞음) |
| 도구 실행 중·응답 중 Esc, Ctrl-C | **없음**(`Stop`도 없음). transcript에 `[Request interrupted by user]`만 남는다 |
| 응답하지 않은 `PermissionRequest` | 6초 뒤 `Notification`(`notification_type: permission_prompt`) |
| 입력 대기 | `Stop` 60초 뒤 `Notification`(`idle_prompt`). `title` 필드는 없다 |
| API 오류 | `Stop` 대신 `StopFailure`(v2.1.78, `error: model_not_found` 등) |
| `/compact` | `PreCompact` → `SubagentStop`(agent_type "") → `SessionStart`(source `compact`, 같은 세션) → `PostCompact`. `Stop`은 없다 |
| 서브에이전트 | 같은 `session_id`에 `agent_id`·`agent_type`. 백그라운드 서브에이전트는 메인 `Stop` 뒤에도 `PermissionRequest`를 내고, 그동안 터미널에는 대화상자가 안 뜬다. 끝나면 합성 `UserPromptSubmit`(`<task-notification>`) → `Stop` |
| 내부 fork(압축 요약, 프롬프트 제안 등) | `agent_id`만 있고 `agent_type` 키가 없는 `PreToolUse`. 그 도구는 실행되지 않는다 |
| 종료 reason | `/exit`·Ctrl-C 두 번 `prompt_input_exit`, SIGHUP·SIGTERM·터미널 닫힘 `other`, `/clear`는 같은 프로세스에서 이전 세션 `clear` → 새 세션 `SessionStart(clear)` |
| settings.json의 모르는 훅 이벤트 키 | 2.1.290은 경고 창을 띄우고 나머지는 적용한다. **2.1.101 전에는 파일 전체를 무시한다**(CHANGELOG) |

**상태 기계 (`internal/session`, `internal/core`)**
- `Notification`은 `notification_type`으로 나눈다: `permission_prompt` → 승인 대기(이전에는 입력 대기로 덮어써졌음), `idle_prompt`·MCP elicitation → 입력 대기, elicitation 완료 → 실행 중, 그 밖(`auth_success`, `claude agents`용 `agent_needs_input` 등)은 그대로. 필드가 없는 예전 버전은 승인 대기가 아닐 때만 입력 대기.
- `StopFailure` → 입력 대기. F-06 훅 등록에 `StopFailure`를 더했다(10개).
- `SessionStart(compact)`는 상태를 바꾸지 않는다. `AskUserQuestion`의 `PermissionRequest`는 입력 대기.
- fork 이벤트(`agent_id`만 있음)는 상태와 게이트웨이 세션 연결에 쓰지 않는다.
- 종료된 세션은 `SessionStart`나 다른 claude 프로세스(`--resume`)의 이벤트로만 다시 열린다. 같은 프로세스가 늦게 보낸 이벤트(SessionEnd는 비동기)는 무시한다.
- 사용자가 턴을 멈춘 것(Esc·Ctrl-C·터미널 거부): (1) 기다리던 훅이 끊기면 메인 에이전트 세션을 입력 대기로, (2) 3초마다 실행 중·승인 대기 세션의 transcript 끝을 읽어, 마지막 이벤트 때의 파일 크기 뒤에 중단 표시가 있으면 입력 대기로 바꾼다. 쓰는 중인 마지막 줄은 판단하지 않는다.
- 터미널에서 Yes로 답하면 같은 도구·같은 입력(정규화 해시)의 `PostToolUse`(또는 메인 `Stop`·`UserPromptSubmit`·`SessionEnd`)가 farero 카드를 거두고, 훅은 결정 없이 끝난다. 로그는 `cancelled` + `answered_in_agent`.
- '알 수 없음': 에이전트 프로세스가 5초 넘게 없으면 바꾼다(아키텍처 16장의 "10분 무이벤트 + 프로세스 없음"에서 변경). `SessionEnd` 없이 끝나는 경우가 있고, PID 재사용은 프로세스 시작 시각으로 거른다. PID를 모르면 10분 무이벤트. claude가 먼저 끝나 launchd에 입양된 훅은 PID 0으로 보낸다.
- 세션 저장·알림은 발행 잠금 안에서 최신 상태를 다시 읽어 보내서, 동시에 바뀌어도 DB와 앱이 옛 상태로 끝나지 않는다.

**F-06 (`internal/agentcfg`)**
- "설정 제거"는 처음 등록하기 전의 settings.json을 기억했다가, 남은 내용이 같으면(키 순서·서식 무시) 바이트 그대로 되돌린다. farero가 만든 파일이면 지운다. 그사이 사용자가 바꾼 내용은 유지한다.
- Claude Code가 2.1.203 미만이면 계획·등록을 거부한다(2.1.101 전에는 새 이벤트 키 때문에 사용자 설정 전체가 무시될 수 있음).
- 앱 위치가 바뀌었을 때 자동 수정(Q63)은 오래된 부분만, 경로만 고친다(새 이벤트를 더하지 않음). 이벤트가 늘어난 뒤에도 고친다(이전에는 훅을 아예 건너뜀). 알림에 백업 경로를 넣고, 고친 즉시 앱에 `agentcfg.status`를 보낸다. settings.json 쓰기는 잠금 + 같은 폴더 임시 파일.
- 앱: 메뉴바에 Claude Code 버전 경고와 경로 수정 알림을 띄운다(설정 화면을 열지 않아도 보임). 연결·재연결 때 `agentcfg.status`를 한 번 묻는다.

**리뷰(서브 에이전트) 반영:** 고아 훅의 PID 1, 이전 턴의 중단 표시 오인, 알림 뒤 중단 누락, 상태 발행 순서, 서브에이전트 훅 종료가 세션 전체를 바꾸던 것, 재사용 PID의 연결 매핑 삭제, settings.json 동시 쓰기, FixPath 반복 알림을 고쳤다. 시계가 뒤로 크게 바뀌면 살아 있는 세션이 잠시 '알 수 없음'이 될 수 있는 점은 남겼다(다음 이벤트에 돌아온다).

**실제 검증 (`scripts/e2e-sessions.py`, Claude Code 2.1.290·2.1.291, haiku).** 개발 데몬과 격리된 Claude 설정 폴더에 실제로 등록한 뒤, 대화형 claude 세 개를 pty로 띄워 사용자처럼 입력한다(터미널 창 없음, 사용자 `~/.claude` 설정은 건드리지 않음). 최종 실행 68개 검사 모두 통과.
- 등록: 훅 10개(StopFailure 포함, timeout 660), allow 규칙, MCP 항목 timeout 660000.
- **M2 완료 기준:** 세션 두 개(alpha, beta)가 따로 보이고(cwd·에이전트·시작 시각·pid·tty), 둘 다 실행 중 → 입력 대기 → 실행 중 → 입력 대기로 오간다. "설정 제거" 뒤 settings.json이 등록 전 파일과 바이트 단위로 같고 MCP 항목도 지워진다.
- Esc(훅 없음) → 입력 대기 0.4~2.9초. 터미널 No → 카드 철회·입력 대기 0.1초, `permission_prompt`(카드 뒤 6.0초)가 와도 승인 대기 유지. 터미널 Yes → 카드 철회, 명령 실행, 로그 `cancelled`/`answered_in_agent`.
- `/exit` → `SessionEnd`로 종료(프로세스 종료보다 약 0.4초 먼저). kill -9(SessionEnd 없음) → 6.6~8.1초 뒤 '알 수 없음'.
- 첫 실행에서 찾은 버그(터미널 No 뒤 `current_tool`이 남음)는 고쳤다.
- 이번 검증에서는 실험의 "훅 대기 중 `/exit`하면 SessionEnd가 없다"가 재현되지 않았다. farerod가 `PostToolUse`·`Stop`에서 대기를 정리해 턴이 끝난 뒤까지 훅이 남지 않고, 도구 실행 중 `/exit`는 훅을 먼저 끝낸 뒤 SessionEnd를 보낸다.

**M3로 넘긴 것 (에이전트 도구 승인·감사 로그)**
- 터미널에서 Yes를 누른 뒤 명령이 끝나기(PostToolUse) 전에 앱에서 거부하면, 로그에 `denied`가 남지만 명령은 실행된다. 터미널 허용을 그 순간 알 신호가 없다.
- 터미널에서 허용한 명령이 도는 중에 Esc·`/exit`로 훅이 끝나면 `cancelled`(이유 없음)로 남아, 터미널 거부와 구별되지 않는다.
- 확인 못 한 것: 실제 `StopFailure` 이벤트로의 전환(단위 테스트만), 서브에이전트 대기 e2e, 앱 화면(노치 세션 목록)을 눈으로 확인하는 것. 앱 상태 처리는 FareroCore 테스트와 devctl(같은 IPC)로 확인했다.

### M3 에이전트 도구 승인 (2026-10-06)
M3 코드(`PermissionRequest` → 승인 카드 → `decision.behavior`, 승인 큐·10분 마감·UI 없으면 `none`, 도구 + 같은 입력 단위 세션 허용, `calls`에 에이전트 도구 판단 기록, 노치 승인 카드·단축키)는 대부분 이미 있었다. 브랜치는 develop(M2) 위에서 시작했다(`seongj-un/m3`가 main 기준이라 develop으로 옮김). M2에서 넘긴 문제를 고치고, 실제 Claude Code로 완료 기준을 확인했다.

**확인한 사실 (Claude Code 2.1.291, M2 실험 기록 재분석 포함)**
- `PermissionRequest` 입력에는 `tool_use_id`가 없다. 바로 앞 `PreToolUse`(같은 도구·입력)와 그 도구의 `PostToolUse`·`PostToolUseFailure`에는 있다. `PostToolUse`에는 `duration_ms`(실행 시간, 권한 프롬프트 제외)도 있다.
- 훅이 거부(deny)하면 그 도구 사용에 대한 `PostToolUse`·`PostToolUseFailure`는 오지 않는다(거부 → 바로 `Stop`).
- 실행 중인 도구를 Esc로 멈추면 Claude Code는 transcript에 터미널 거부와 같은 `User rejected tool use`를 남기고 훅 이벤트도 보내지 않는다. 그래서 "터미널에서 허용한 뒤 Esc"와 "터미널에서 거부"는 구별하지 않고 둘 다 `cancelled`(이유 없음)로 둔다(Claude Code 자신도 거부로 기록).

**고친 것**
- **터미널에서 먼저 허용한 뒤 앱에서 거부(또는 시간 초과):** Claude Code는 늦게 온 훅의 deny를 무시하고 도구를 실행한다. farerod가 `PreToolUse`의 `tool_use_id`를 기억해 `PermissionRequest`에 붙이고, farero가 거부한 도구 사용의 `PostToolUse(Failure)`가 오면 로그 줄을 `cancelled`/`answered_in_agent`로 고친다(`internal/core/agentwait.go`). 앱의 답이 도착하기 직전에 에이전트가 이미 진행한 경우(같은 순간의 경쟁)도 같은 결과로 남긴다. 세션 허용 답이었으면 허용 기록은 남긴다. 다시 시도한 같은 명령은 새 `tool_use_id`라 앞의 거부 줄은 그대로다.
- 카드를 거두는 `PostToolUse` 매칭에 `tool_use_id`를 더해, 같은 명령의 이전 실행이 끝난 것으로 새 카드가 닫히지 않게 했다.
- **소켓 JSON의 HTML 이스케이프:** `farero-hook` → `farerod` 전송이 `encoding/json` 기본값이라 도구 입력의 `&&`·`>>`가 `&&`·`>>`로 바뀐 채 감사 로그에 저장됐다. 로그 검색에서 `&&`로 찾을 수 없었다. 소켓과 앱 알림 모두 이스케이프하지 않는다(`ipc.Marshal`). 이미 저장된 줄은 그대로다.
- 개발용: `farerod --dev`에서 `FARERO_APPROVAL_TIMEOUT`(예: `30s`)으로 승인 마감을 줄일 수 있다. `farero-devctl answer <id> allow|allow_session|deny`로 특정 카드에 답한다.

**실제 검증 (`scripts/e2e-approvals.py`)**
M2 하네스(pty·화면·격리 설정)를 `scripts/e2elib.py`로 분리해 `e2e-sessions.py`와 같이 쓴다. 분리 뒤 `e2e-sessions.py`(M2)도 다시 통과했다(64개).

`scripts/e2e-approvals.py` (Claude Code 2.1.291, haiku, 격리 설정, 앱 대신 `farero-devctl`). 마지막 실행 67개 검사 모두 통과.

| 확인 | 결과 |
|---|---|
| 시나리오 A: `npm install` | 카드(에이전트 도구 Bash, 이유 `agent_request`, 세션 허용 버튼, 입력 전체, 마감)와 터미널 프롬프트가 함께 뜸 → 카드에서 허용 → 터미널 프롬프트가 바로 닫히고("Allowed by PermissionRequest hook") npm 실행 → `user_allowed` |
| 세션 허용 범위(Q38) | `echo hit >> grant.log`를 "이번 세션 동안 허용" → 같은 명령은 카드·프롬프트 없이 실행(`session_allowed`) → 다른 명령은 다시 물음 → 카드에서 거부하면 실행되지 않고 모델이 "farero: 사용자가 거부함"을 받음(`denied`) |
| 터미널 허용 뒤 앱 거부 | 터미널 Yes → 도구가 도는 동안 카드가 그대로 → 카드에서 거부 → 도구는 끝까지 실행됨 → 로그가 `denied`에서 `cancelled`/`answered_in_agent`로 바뀜(한 줄) |
| 승인 마감 | 개발용 30초: 30.0초에 `timeout`으로 카드가 거둬지고 훅이 거부, 명령 미실행, 모델이 "승인 대기 시간 초과"를 받음. **실제 10분(`E2E_APPROVAL_TIMEOUT=10m`)도 통과: 600.0초에 같은 결과, Claude Code가 훅(timeout 660)을 먼저 끊지 않음** |
| 앱 꺼짐 | 카드 없이 훅이 아무것도 출력하지 않음(`passthrough`/`app_not_running`) → 터미널 프롬프트에서 Yes → 실행 |
| `StopFailure` | 없는 모델(`--model claude-nonexistent-farero`)로 프롬프트 → `Stop` 없이 `StopFailure` → running → waiting_input. M2에서 단위 테스트로만 확인했던 항목 |
| farero 거부 뒤 | 그 도구 사용의 `PostToolUse(Failure)`는 오지 않음 → 거부·시간 초과 줄은 그대로 |

- 스크립트에서 고친 것: 터미널 프롬프트가 열린 직전에는 첫 선택지 줄에 진행 표시가 겹쳐 있어("1. …(running PreToolUse hook)") "Yes" 선택을 기다렸다가 읽는다. 알 수 없는 모델이면 하단에 "? for shortcuts" 대신 권한 모드("auto mode on")가 보여서 입력 대기 판정에 넣었다.
- 확인 못 한 것: 노치 카드의 단축키(⌃⌥Y/S/N)와 클릭을 실제 앱 번들에서 누르는 것, 포커스를 빼앗지 않는지 눈으로 보는 것(M0 2차에서 카드로 두 건 처리한 기록은 있음). 서브에이전트의 승인 대기 e2e.

## 2026-10-08

### M4 게이트웨이
M4 코드(MCP 게이트웨이, 세션 연결, 정책 엔진, 감사 로그, 개발용 가짜 플러그인, `farero-hook --headers`, F-06 게이트웨이 등록)는 대부분 이미 있었다. 브랜치는 develop(M3) 위에서 시작했다(`seongj-un/m4`가 main 기준이라 develop으로 옮김). 실제 대화형 Claude Code로 완료 기준을 확인하면서 드러난 문제를 고쳤다.

**확인한 사실 (Claude Code 2.1.293, macOS 26.6.2)**
- **프로토콜:** 게이트웨이가 2026-07-28을 받게 되자(아래 "고친 것"), Claude Code는 시작 0.8~1.2초 뒤 `server/discover`(2026-07-28, client `claude-code 2.1.293`)를 보낸다. 이어서 같은 연결로 `subscriptions/listen`과 `tools/list`를 부르고, `initialize`는 보내지 않는다. 그 전(M0)에는 SDK가 2026-07-28을 거절해서 `initialize`(2025-11-25)로 접속했다.
- **도구 목록 변경:** 정책으로 도구를 차단하면, 열려 있던 세션 두 개가 0.02~0.04초 뒤 `tools/list`를 다시 읽는다. 다음 프롬프트에서 transcript에 `deferred_tools_delta`(removedNames)가 붙고, 모델은 그 도구를 부르지 않는다(PreToolUse·호출·로그 없음). 되돌리면 0.03초 안에 다시 읽고, 그 도구가 다시 동작한다.
- MCP 도구는 deferred라서, 모델이 도구를 처음 쓰기 전마다 `ToolSearch select:<도구>`를 먼저 부른다.
- **120초 백그라운드 이동:** MCP 호출이 120초 넘게 끝나지 않으면 Claude Code가 그 호출을 백그라운드로 옮긴다. 모델은 바로 "still running after 120s. It was moved to the background as task …"(오류 아님)를 받는다. 그 도구의 `PostToolUse`가 120.1초에, `Stop`이 약 122초에 와서 턴이 끝나지만, 카드는 그대로 열려 있다.
  - 나중에 허용하면 0.2초 뒤 `<task-notification> completed … wrote: …`가 새 턴으로 들어간다.
  - 시간 초과면 `failed … 승인 대기 시간 초과 (farero)`가 새 턴으로 들어간다.
  - Claude Code 번들의 `getMcpAutoBackgroundMs`(기본 120000ms, 플래그 `tengu_mcp_auto_background`, 환경 변수 `CLAUDE_CODE_MCP_AUTO_BACKGROUND_MS`)가 이 동작을 정한다. `-p`와 IDE 서버에서는 꺼져 있고, MCP elicitation이 열려 있는 동안에도 일어나지 않는다.
  - 환경 변수를 0으로 두면 옮기지 않는다. 그러면 150초 허용 결과가 바로 돌아오고, 180초 시간 초과는 도구 오류와 `PostToolUseFailure`로 끝난다.
- **결정 (사용자, Q83):** 백그라운드 이동은 Claude Code 기본값대로 둔다. F-06은 환경 변수를 넣지 않는다. 승인 규칙(승인 전에는 실행하지 않음, 10분 마감)은 그대로 지켜지고, 결과만 새 턴으로 전달된다.
- **실제 10분:** Claude Code는 HTTP 요청을 600초 동안 열어 두었다. 카드는 600.0초에 `timeout`으로 거둬졌다(행 1개, `tools/call` 1번, duration_ms 600005~600015). 서버별 `timeout: 660000` 덕분에 5분 idle 타임아웃이 먼저 끊지 않는다. M0에서는 75초까지만 확인했었다.
- **승인 대기 중 Esc:** 카드가 0.05~0.06초 뒤 거둬지고, 행은 `cancelled`다. 훅 이벤트는 없다(PostToolUse(Failure)도 Stop도 없음). transcript에는 "The user doesn't want to proceed…"와 중단 표시가 남는다. 상태는 1.1~3초 뒤 입력 대기가 된다(transcript 확인).
- **카드가 열린 채 입력:** 입력하고 0.6초 뒤 `UserPromptSubmit`이 온다. 이때 호출은 백그라운드로 옮겨지지 않고, 허용하면 결과가 그대로 돌아온다.
- **PreToolUse → 게이트웨이 호출:** 3~14ms(중앙값 5~6ms)로, M0의 60~90ms보다 짧다. `headersHelper`는 claude 프로세스마다 한 번 실행되고, 연결 ID 하나가 세션 내내(요청 22~23개) 쓰이며 세션과 1:1로 대응한다.
- **farerod 재시작**(같은 포트·비밀값, 앱 업데이트 때의 `kickstart -k`와 같음): Claude Code는 2.0초 뒤 같은 연결 ID로 `subscriptions/listen`을 다시 열고 `tools/list`를 다시 읽는다. `server/discover`도 `headersHelper`도 다시 실행하지 않는다(2026-07-28은 세션이 없어 잃을 것이 없음). 다음 호출은 새 farerod에서 같은 세션에 연결됐다.
- **auto mode:** 격리한 설정(사용자 설정 없음)에서 Claude Code 2.1.293이 auto mode로 시작했다. 분류기가 허용한 Bash(`npm install`, `echo q >> grant.log`)는 `PermissionRequest` 없이 실행돼 farero 카드가 뜨지 않는다. F-04 카드는 Claude Code가 실제로 물을 때만 뜬다.
  - 권한 프롬프트에 "Yes, and switch to auto mode"가 더해져 "No"가 네 번째 선택지가 됐다.
  - e2e 스크립트는 `--permission-mode default`로 실행하고, 선택지는 글자로 찾는다(`e2elib.select_option`).
- 이전 프로토콜 클라이언트(`initialize` 2025-11-25)도 그대로 동작한다. Mcp-Session-Id와 SSE 응답을 받는다.

**고친 것**
- **게이트웨이가 2026-07-28도 받는다** (기능 명세서 5-6): Go MCP SDK v1.8.0은 2026-07-28을 stateless 핸들러에서만 받고, stateless 핸들러는 `initialize` 클라이언트에게 세션을 주지 않는다(그러면 `tools/list_changed`를 못 받는다).
  - 그래서 요청을 나눈다. `Mcp-Session-Id`가 없고 `Mcp-Protocol-Version`이 2026-07-28 이상이면 stateless 핸들러로, 나머지(`initialize`, 세션 헤더가 있는 요청, GET 알림 스트림)는 stateful 핸들러로 보낸다. 두 핸들러는 같은 서버를 쓴다.
  - stateless 쪽은 `PropagateRequestCancellation`을 켰다. 에이전트가 요청을 버리면(Esc) 호출도 끝나 카드가 거둬진다.
  - 요청 크기 한도는 4MiB에서 64MiB로 늘렸다(Resend 첨부 메일).
- **카드가 열린 동안 세션은 승인 대기:** 위의 백그라운드 이동(PostToolUse·Stop)과 카드가 열린 채의 입력(UserPromptSubmit)이 `waiting_approval`을 덮어썼다. 또 게이트웨이가 카드가 끝나면 무조건 `running`으로 바꿔서, 시간 초과·취소 뒤 쉬고 있는 세션이 실행 중으로 보였다.
  - 이제 세션별로 열린 카드 수를 세고(`session.ApprovalOpened/Closed`, 메모리만), 카드가 있으면 `waiting_approval`, 없으면 훅 이벤트가 정한 상태를 보여 준다. 에이전트 도구 카드도 같다.
  - DB에는 훅 이벤트가 정한 상태를 저장한다. 카드는 farerod가 재시작되면 사라지므로, 다시 읽었을 때 없는 카드를 기다리는 것처럼 보이면 안 된다. 세션을 삭제해도 열린 카드 수는 남긴다(같은 세션이 다시 생겨도 맞게 보이도록).
- **세션 연결:** farerod를 재시작하면 연결 ID → 에이전트 PID 대응(동시 같은 인자 호출의 구분용)이 사라졌다. 메모리에만 있었고, Claude Code가 `headersHelper`를 다시 실행하지 않기 때문이다. 이제 연결 ID 자체에 PID를 넣는다(`<pid>.<난수>`, `correlate.NewConnID`). 처음에는 모호하지 않은 매칭에서 PID를 배우게 했지만, 리뷰에서 문제가 나와 바꿨다. 자기 PreToolUse가 오지 않은 호출이 다른 세션의 같은 키에 묶이면, 그 PID를 잘못 배운 채 남는다. 그러면 이후 모호한 호출이 다른 세션의 세션 허용을 받을 수 있다.
- 개발용: `farero-devctl policy set <plugin> <tool> [level]`. 게이트웨이가 MCP 요청을 로그에 남긴다(연결 설정은 INFO, 나머지는 `--debug`).
- 테스트: 두 프로토콜 각각의 목록·호출·연결 헤더·`list_changed`·취소, 카드가 열린 동안의 상태(백그라운드 이동, 새 입력, 시간 초과, DB 저장, 삭제), 연결 ID의 PID, 분류표 도구 이름이 Claude 도구 이름 규칙(64자)에 맞는지.
- 리뷰(서브 에이전트) 반영: 위의 PID 학습 문제, 세션 삭제 때 카드 수가 사라지던 것, 카드 상태가 DB에 저장되던 것, e2e에서 빈 목록이면 통과하는 검사와 카드가 열린 동안 훅 이벤트가 실제로 왔는지 보지 않던 검사.

**실제 검증 (`scripts/e2e-gateway.py`)**
개발 데몬(가짜 플러그인 `dev`)을 격리된 Claude 설정에 실제로 등록하고, 대화형 claude 두 개를 pty로 띄워 돌린다. `alpha`는 정상 세션이고, `beta`는 PreToolUse 훅을 뺀 세션이라 세션 불명이 된다. 앱 대신 `farero-devctl`로 카드에 답한다. 마지막 실행 기준으로 빠른 실행은 136개, 3분 마감 실행(백그라운드 이동 포함)은 33개 검사가 모두 통과했다. 실제 10분 실행은 상태 수정 전의 스크립트로 23개 검사가 통과했다. 바뀐 하네스로 `e2e-sessions.py`(64개)와 `e2e-approvals.py`(67개)도 다시 통과했다.

| 확인 | 결과 |
|---|---|
| HTTP | 127.0.0.1의 저장된 포트에만 열림, `Origin` 헤더 403, bearer 없음·틀림 401 |
| 도구 목록 | `dev_echo/write_sim/destroy_sim/taint_sim/fail_sim`만 보임(`blocked_sim`·`unclassified_sim` 없음). annotation은 분류표 기준(readOnly는 echo·taint_sim, destructive는 destroy_sim만) |
| 자동 허용 | 카드 없이 `auto_allowed`, alpha 세션·연결 ID에 연결 |
| 승인 + 세션 허용 | 카드(플러그인·도구·입력 전체·이유 `policy`·세션 허용 버튼·폴더 이름) → `allow_session` → 다음 호출 `session_allowed` |
| 세션 허용 불가 | 버튼 없음, `allow_session` 답은 거절됨, 매번 다시 물음 |
| 거부·업스트림 실패 | 모델이 `is_error`와 이유를 받음, 실패는 `error`가 기록되고 호출 1번(재시도 없음) |
| 오염 (시나리오 C) | `taint_sim` 뒤 `tainted: true`. `write_sim`은 이유 `policy,tainted`로 버튼 없이 다시 묻고, 세션 허용해 둔 Bash도 다시 물음(Q39) |
| 앱 꺼짐 (시나리오 D) | `dev_echo`는 동작, `write_sim`은 `auto_denied`/`app_not_running`, 모델이 "실행 중이 아니라"를 받음 |
| 세션 불명 | beta의 카드에 `unknown_session`, 버튼 없음, "세션 불명", 행의 session_id 없음 |
| 승인 마감 | 30초·3분·10분 모두 `timeout`, 카드 철회, 모델이 "승인 대기 시간 초과"를 받음(도구 오류 또는 task-notification) |
| 세션 상태 | 카드가 열린 동안 `waiting_approval`(입력·백그라운드 이동·Stop에도 유지), 닫히면 훅 이벤트대로(허용 → running, 턴이 끝났으면 waiting_input), Esc → 1.1초 뒤 waiting_input |
| 재시작·목록 변경 | 위 사실대로 |

- 확인 못 한 것: 재시작을 넘어 두 세션이 같은 인자를 동시에 보낼 때의 PID 구분을 실제 Claude Code로 확인하는 것(단위 테스트만). 승인 카드를 실제 앱 번들에서 누르는 것(M6).

### M5 플러그인
M5 코드(4개 플러그인 연결, 토큰 저장·갱신, 앱 플러그인 창)는 대부분 이미 있었다. 브랜치는 develop(M4 병합) 위에서 시작했다. 실제 계정으로 `tools/list`를 받아 분류표를 확정하고, 실제 대화형 Claude Code로 시나리오 B·C·D를 확인했다. 사용자 결정(2026-10-08):
- GitHub 쓰기 확인은 새 비공개 저장소 `farero-dev/farero-e2e`에서 한다(만든 이슈는 끝에 `gh`로 닫음).
- Railway는 계정 체험 기간이 끝나 프로젝트를 만들 수 없다. 시나리오 B는 카드 → 허용 → 업스트림 호출 → 로그까지만 확인한다(실제 재배포는 나중에).
- Gmail은 GCP OAuth 클라이언트가 생기면 따로 한다. 그동안 시나리오 C의 오염 소스는 GitHub `issue_read`로 확인한다.
- Resend는 보류한다(이 네트워크에서 `resend.com` 로그인 페이지가 여전히 열리지 않음). 분류표는 resend-mcp 소스(`0047400`, 2026-10-07)로 맞췄다.

**확인한 사실 (Claude Code 2.1.293, macOS 26.6.2)**
- **GitHub** (기본 toolset, device flow 토큰): 도구 44개. 분류표와 같고, 새 도구는 `ui_get` 하나다. MCP Apps 화면 전용(`_meta.ui.visibility: ["app"]`)이라 에이전트에 노출하지 않는다(차단). 분류표의 `delete_repository`, `assign_copilot_to_issue`, `request_copilot_review`는 기본 toolset에 없다(차단 항목이라 그대로 둔다).
- **GitHub 읽기 전용 스위치가 동작하지 않았다:** 코드가 `X-MCP-Read-Only` 헤더를 보냈는데, 원격 서버의 헤더 이름은 `X-MCP-Readonly`다(docs/remote-server.md). 모르는 헤더는 조용히 무시해서 쓰기 도구 17개가 그대로 보였다. 고친 뒤 켜면 27개(쓰기 0개, 노출 26개)로 줄고, 끄면 44개로 돌아온다.
- **Railway** (DCR + loopback 토큰): 도구가 68개로 늘었다(분류표 11개). 분류하지 않은 57개는 숨겨져 있었다. 도구 설명에 따르면 OAuth 앱에는 `list-variables`가 변수 이름만, `get-bucket-credentials`가 키 없이 접속 정보만 준다. 업스트림 annotation은 `redeploy`를 destructive로 표시하지만 노출 annotation은 분류표를 따른다(Q36).
- **Resend** (소스 기준): 분류표의 메일 도구 15개는 이름 그대로 있다. 메일 관련 새 도구는 `share-email`(보낸·받은 메일의 공개 공유 링크, 48시간) 하나다.
- **토큰 갱신:** 저장된 토큰의 만료 시각을 과거로 바꾸고 farerod를 띄우자, GitHub(만료형 사용자 토큰, 8시간)와 Railway(1시간) 모두 실제 서버에서 갱신됐다. 둘 다 refresh token도 새 값으로 바뀌어 저장됐고, 연결 상태와 도구 목록 조회가 그대로 유지됐다. M0에서 남긴 "GitHub 만료형 토큰 자동 갱신(8시간 뒤)" 확인을 대신한다.
- **Railway 업스트림 오류:** 없는 ID로 `redeploy`를 부르면 Railway가 200ms 안에 "You don't have the required role (member) on this resource."를 돌려준다. 행은 `user_allowed`에 오류 "upstream returned an error"이고, `tools/call`은 1번이다(재시도 없음).
- Railway `whoami`의 첫 줄은 Markdown(`**이름** (@login)`)이라 계정 표시에 별표가 그대로 남았다.

**분류표 확정 (사용자 결정, 2026-10-08)**
- Railway 새 도구 57개 (리뷰 뒤 일부를 올림, 아래 "리뷰 반영"):
  - 자동 허용(23): 조회·상태·지표·문서·템플릿 검색.
  - 자동 허용 + 오염(5): `get-logs`, `list-traces`, `get-trace`, `get-deployment-diagnosis`, `describe-template`. 외부 요청 내용, 커밋·PR 글, 커뮤니티 README가 섞인다.
  - 승인(14): 만들기·바꾸기·`restart-service`, `get-bucket-credentials`.
  - 승인, 세션 허용 불가(15): 삭제 6종, `reset-bucket-credentials`, `set-variables`(덮어쓴 값은 되돌릴 수 없음), `update-function-source-code`(코드 전체 덮어쓰기), `update-service`·`connect-service-source`(시작 명령·소스를 바꾸면 서비스의 비밀 변수로 임의 코드가 돈다), `create-webhook`·`update-webhook`·`test-webhook`(임의 URL로 POST), `create-tcp-proxy`(DB 등을 인터넷에 노출).
  - Railway 전체: 자동 허용 28, 자동 허용 + 오염 5, 승인 17, 세션 허용 불가 17, 차단 1.
- GitHub `ui_get`과 Resend `share-email`은 차단으로 적었다. 분류표에 없어도 숨겨지지만, 결정을 남기기 위해서다.

**고친 것**
- GitHub 읽기 전용 헤더를 `X-MCP-Readonly`로 고쳤다.
- 계정 표시: Markdown 강조를 지우고, 바이트가 아니라 글자 단위로 자른다(한글이 중간에 잘리지 않게).
- 개발용 `farero-devctl plugin [connect <p> [k=v…] | disconnect <p> | option <p> <k> <v> | tools <p>]`를 추가했다. connect는 앱의 플러그인 창처럼 device code·URL을 보여 주고 브라우저를 연 뒤, 연결되거나 실패할 때까지 기다린다.
- IPC `plugin.tools`를 추가했다. 연결된 플러그인의 실제 `tools/list`(설명·업스트림 annotation 포함)를 분류표와 나란히 보여 주고, 분류표에는 있지만 업스트림에 없는 도구도 알려 준다. farerod는 분류표에 없는 업스트림 도구를 목록이 바뀔 때마다 INFO로 남긴다(업스트림 변화 감지용, 도구는 계속 숨김).
- 테스트: `plugin.tools`(미분류·차단·없어진 도구), 계정 표시.
- 리뷰(서브 에이전트) 반영:
  - 분류 (사용자 결정, 2026-10-08): 플러그인 세션 허용은 입력과 상관없이 도구 단위라, 한 번 허용하면 그 세션에서는 모든 프로젝트·서비스에 다시 묻지 않는다. 그래서 `update-service`, `connect-service-source`, `create-webhook`, `update-webhook`을 세션 허용 불가로 올렸다. `get-bucket-credentials`는 "OAuth 앱에는 키 없음"이 도구 설명일 뿐 실제로 확인하지 못해 승인으로 올렸다.
  - 업스트림 목록 조회가 호출자의 시간 초과로 끝나도 공용 MCP 세션을 닫지 않는다. 닫으면 같은 세션으로 진행 중이던 에이전트 호출이 실패하는데, 이 호출은 재시도하지 않으므로 업스트림에서는 실행됐을 수 있다.
  - e2e:
    - 이 디렉터리의 소켓을 연 farerod만 멈춘다. 다른 worktree·e2e·설치된 앱의 farerod는 건드리지 않는다.
    - `gh` 로그인을 먼저 확인한다(빈 로그인이면 결과 검사가 항상 참이 됐다).
    - 목록에 없는 도구를 통과시키던 annotation 검사를 고쳤다.
    - 앱이 꺼진 동안의 턴 종료는 DB의 `Stop`으로 기다린다(전에는 매번 120초를 다 기다렸다).

**실제 검증 (`scripts/e2e-plugins.py`)**
개발 데몬을 `/tmp/frm5`에서 띄운다. 실행마다 `dev-secrets.json`과 `farero.db`만 남기고 지워서, 실행 때마다 다시 로그인하지 않게 했다. 연결되지 않은 플러그인은 처음에 대화형으로 연결한다. farero를 격리된 Claude 설정에 등록하고, 대화형 claude 하나(`alpha`)로 실제 GitHub·Railway 도구를 게이트웨이를 거쳐 부른다. 공용 헬퍼(transcript, 게이트웨이 원시 클라이언트, farerod 로그 읽기)는 `e2elib.py`로 옮겼다. 검사 72개가 모두 통과했다.

| 확인 | 결과 |
|---|---|
| 분류표 | GitHub 44개 중 미분류 0(노출 43, 차단 `ui_get`), Railway 68개 중 미분류 0·빠진 항목 0(노출 67, 차단 `railway-agent`) |
| 도구 목록 | 원시 MCP 클라이언트로 github 43 + railway 67. annotation은 분류표 기준 |
| 자동 허용 | `github_get_me`(412ms, `gh` 로그인과 같은 계정), `railway_whoami`(158ms). 카드 없음, alpha 세션·PID가 든 연결 ID |
| 시나리오 B | `railway_redeploy` 카드(플러그인·도구·입력 전체·이유 `policy`·세션 허용 버튼·alpha, 세션은 승인 대기) → 허용 → 업스트림 호출 1번 → 모델이 Railway 오류를 받음, 행 `user_allowed` + 오류 |
| 시나리오 C | `issue_write` 세션 허용 → 이슈 A 생성 → 이슈 B는 `session_allowed`(카드 없음) → `issue_read`로 오염 → 이슈 C 카드는 이유 `policy,tainted`·버튼 없음 → 거부, 이슈 C 없음 |
| 시나리오 D | 앱 꺼짐: `get_me`는 동작, `create_pull_request`는 `auto_denied`/`app_not_running`, 모델이 "실행 중이 아니라"를 받음, PR 없음 |
| 읽기 전용 | 켜면 github 26개(쓰기 0), 끄면 43개(쓰기 17). Claude Code가 두 번 모두 `tools/list`를 다시 읽음 |

- 확인 못 한 것: 실제 Railway 재배포(프로젝트 없음), Gmail(GCP 클라이언트 필요, 시나리오 C의 메일 오염), Resend 연결.
