# 릴리스 체크리스트

두 부분이다. (a) 릴리스 절차, (b) 깨끗한 계정에서 Releases 파일로 시나리오 A–E를 확인하는 절차(M8 완료 기준). 먼저 `v0.1.0-beta.1`(프리릴리스)로 (b)까지 통과시키고, 문제가 없으면 `v0.1.0`을 낸다.

## a) 릴리스 절차

태그 이름
- 정식: `vX.Y.Z` (예: `v0.1.0`)
- 프리릴리스: `vX.Y.Z-beta.N` (예: `v0.1.0-beta.1`). 태그에 `-`가 있으면 워크플로가 `--prerelease`로 게시한다.

- [ ] `develop`이 최신이고 CI(`ci`)가 초록인지 확인한다.
- [ ] 태그를 만들고 푸시한다. `git tag v0.1.0-beta.1 && git push origin v0.1.0-beta.1`
- [ ] 워크플로를 지켜본다. `gh run watch` (또는 `gh run list --workflow release`)
  - 기대: go vet, go test -race, 캐릭터·앱 swift test, 빌드, 체크섬, 게시 단계가 모두 통과한다.
- [ ] `gh release view v0.1.0-beta.1`로 확인한다.
  - [ ] 프리릴리스 표시가 맞다(정식 태그면 표시가 없다).
  - [ ] 자산 2개: `Farero-<버전>.zip`, `Farero-<버전>.zip.sha256`
  - [ ] 노트 맨 위에 요구 사항·설치 방법이 있고 `{{VERSION}}`, `{{BETA_NOTE}}` 같은 자리표시자가 남아 있지 않다. 그 아래에 자동 생성 변경 내역이 붙는다.
- [ ] 브라우저로 zip과 `.sha256`을 받는다(격리 속성이 붙게 하려고 `gh`·`curl` 말고 브라우저를 쓴다).
- [ ] 체크섬을 검증한다. `cd ~/Downloads && shasum -a 256 -c Farero-<버전>.zip.sha256` → `OK`
- [ ] 이 뒤 (b)를 진행한다.

## b) 깨끗한 계정에서 확인

목적: 개발 맥의 설정·Keychain·Claude Code 설정이 섞이지 않은 상태에서 Releases에서 받은 파일만으로 설치해 시나리오 A–E를 통과시킨다.

### 1. 계정 준비

- [ ] 시스템 설정 › 사용자 및 그룹 › 계정 추가 › **표준** 사용자(예: `frtest`)를 만든다. 관리자 아님.
- [ ] 그 계정으로 로그인한다(빠른 사용자 전환).
- [ ] Terminal.app을 연다(점프는 Terminal.app만 지원한다).
- [ ] Claude Code를 설치하고(`claude --version`이 2.1.203 이상) 로그인한다. 설치 방법은 Claude Code 문서를 따른다.
- [ ] 아무 폴더(예: `~/work/demo`)를 만들고 거기서 `claude`가 뜨는지 본다. 신뢰 여부 질문에는 허용한다. 확인 후 종료한다.
- [ ] `~/.claude/settings.json`에 farero 항목이 없고 `claude mcp list`에 `farero`가 없음을 확인한다.

### 2. 내려받기와 Gatekeeper

- [ ] 이 계정의 Safari/Chrome으로 Releases 페이지에서 zip과 `.sha256`을 받는다.
- [ ] `shasum -a 256 -c Farero-<버전>.zip.sha256` → `OK`
- [ ] zip을 푼다(더블클릭). `~/Downloads/Farero.app`이 생긴다.
- [ ] **Translocation 확인.** 아직 옮기지 말고 `~/Downloads/Farero.app`을 연다.
  - 기대: Gatekeeper 차단 창(“확인되지 않은 개발자” 계열)이 뜬다. 닫는다.
  - [ ] 시스템 설정 › 개인정보 보호 및 보안 아래 **그래도 열기**를 누르고 암호를 입력한 뒤 다시 연다.
  - 기대: 앱이 “Applications로 옮겨 달라”는 안내를 띄우고 데몬을 등록하지 않는다. `ls ~/Library/LaunchAgents/dev.farero.farerod.plist`가 없음.
  - (이동 안내가 뜨지 않으면 기록하고 계속한다. README의 설명과 다르면 README 또는 앱을 고친다.)
- [ ] 앱을 종료하고 `Farero.app`을 `/Applications`로 옮긴다(관리자 암호가 필요할 수 있다. 표준 계정이면 `~/Applications`가 아니라 `/Applications`로 옮길 수 있는지 기록한다).
- [ ] `/Applications/Farero.app`을 연다.
  - 기대: 이미 허용했으므로 차단 없이 열린다. 다른 경로로 바뀌어 다시 차단되면 **그래도 열기**를 한 번 더 한다.

### 3. 온보딩

- [ ] 노치(또는 상단 가장자리)에 작은 등대가 나타난다. 메뉴바에 아이콘이 있다.
- [ ] “백그라운드 항목이 추가됨” 알림이 뜬다. 시스템 설정 › 일반 › 로그인 항목 및 확장 프로그램에서 Farero가 켜져 있다.
- [ ] Keychain 확인 창이 뜨는지 기록한다. 첫 설치에서는 farerod가 만든 항목을 자기가 읽으므로 뜨지 않을 수 있다. 뜨면 암호 입력 + **항상 허용**.
  - 기대: 메뉴에 게이트웨이 오류가 없고, **데몬 상태…**가 연결됨을 보인다.
  - 터미널 확인: `launchctl print gui/$(id -u)/dev.farero.farerod | head`
- [ ] **설정 도우미…**(또는 첫 실행 창)에서 Claude Code 등록을 시작한다.
  - [ ] 변경 diff가 보인다(hooks, `permissions.allow`의 `mcp__farero__*`, MCP 서버 `farero`). 내용을 읽고 확인한다.
  - [ ] 확인 뒤 `~/.claude/settings.json`과 `claude mcp list`에 항목이 생기고, `~/Library/Application Support/Farero/backups`에 백업이 있다.
  - [ ] 설정 › Claude Code 탭의 세 항목(훅, 자동 허용 규칙, MCP 서버)이 모두 체크된다.
- [ ] 플러그인 연결(선택). 시나리오 B·C·D를 하려면 아래 4번 준비를 한다. 연결하지 않으면 B·C를 건너뛴다고 기록한다.

### 4. 플러그인 준비 (시나리오 B, C, D용)

`scripts/e2e-plugins.py`가 쓰는 테스트 자원을 그대로 쓴다. 새 계정에서도 같은 외부 계정으로 연결한다.
- GitHub: 저장소 `farero-dev/farero-e2e`(비공개). 이 저장소에 쓸 수 있는 GitHub 계정으로 device flow 연결. 시나리오 C가 이슈를 만든다(끝난 뒤 닫는다).
- Railway: 프로젝트 `farero-e2e`, 환경 `production`, 서비스 `whoami`(이미지 `traefik/whoami`). 브라우저에서 이미 로그인된 Railway로 OAuth 연결.
- Resend(선택): 테스트 수신함 `delivered@resend.dev`로만 보낸다(발신 `onboarding@resend.dev`). Resend 동의 화면은 브라우저에서 이미 로그인된 상태여야 한다.
- Gmail(선택): 본인 GCP OAuth 클라이언트가 필요하다. 이번 확인에서는 건너뛰어도 된다.

- [ ] 메뉴 › **플러그인 연결…**에서 GitHub, Railway를 연결한다. 기대: 상태 “연결됨”.
- [ ] `claude`를 새로 열어 `/mcp`에 `farero`가 연결됨으로 보이고 `github_*`, `railway_*` 도구가 있다.

### 5. 시나리오 (기능 명세서 3장)

각 시나리오는 이 계정의 Terminal.app에서 `cd ~/work/demo && claude --permission-mode default`로 연 세션에서 한다(자동 모드는 카드를 건너뛰므로 쓰지 않는다).

**A. 셸 명령 승인**
- [ ] 프롬프트: `Run this exact shell command with the Bash tool: npm install`
- 기대:
  - [ ] 노치에 승인 카드(도구 Bash, 명령 전체, 폴더 이름)가 뜬다. 포커스를 빼앗지 않는다.
  - [ ] 터미널에도 Claude Code 자체 프롬프트가 떠 있다.
  - [ ] 카드에서 **허용**(또는 ⌃⌥Y)을 누르면 터미널 프롬프트가 닫히고 명령이 실행된다.
  - [ ] 로그 검색…에 해당 호출이 “사용자 허용”으로 남는다.
- [ ] 같은 명령을 한 번 더 시키고 **거부**(⌃⌥N)를 눌러 거부 결과가 모델에 전달되는지 본다.
- [ ] **이번 세션 동안 허용**(⌃⌥S)을 누른 뒤 같은 명령을 다시 시키면 카드가 뜨지 않는다.

**B. 배포 (Railway redeploy)**
- 도구 입력에 프로젝트·서비스·환경 ID가 필요하다. 먼저 `Call the MCP tool mcp__farero__railway_list-projects`, `railway_list-services`로 `farero-e2e`/`whoami`/`production`의 ID를 찾게 한다(자동 허용, 카드 없음).
- [ ] 프롬프트: `Call the MCP tool mcp__farero__railway_redeploy with exactly these arguments: {"projectId":"…","serviceId":"…","environmentId":"…"}.`
- 기대:
  - [ ] 카드에 플러그인 railway, 도구 redeploy, 입력값 전체, 사유(분류표)가 보인다.
  - [ ] **허용** → 재배포가 일어나고(Railway 대시보드에서 새 배포가 SUCCESS) 모델이 결과를 받는다.
  - [ ] 로그에 plugin=railway, tool=redeploy, 판단=사용자 허용이 남는다.
  - 120초 넘게 카드를 두면 Claude Code가 호출을 백그라운드로 옮기고 답은 나중에 알림 턴으로 온다. 이는 정상이다.

**C. 오염 (GitHub 이슈로 확인)**
명세서의 Gmail 예 대신 e2e 스크립트와 같이 GitHub로 한다. Gmail을 연결했다면 메일 읽기로 한 번 더 해 본다.
- [ ] 프롬프트 1: `Call the MCP tool mcp__farero__github_issue_write with exactly these arguments: {"method":"create","owner":"farero-dev","repo":"farero-e2e","title":"release check 1","body":"Safe to close."}` → 카드 → **이번 세션 동안 허용**. 이슈가 만들어진다.
- [ ] 같은 도구로 제목만 바꿔 한 번 더 → 카드 없이 실행된다(세션 허용).
- [ ] 프롬프트 2: `Call the MCP tool mcp__farero__github_issue_read ...`(위에서 만든 이슈 번호, `method` `get`). 자동 허용, 카드 없음. 세션이 오염되어 캐릭터가 “조심” 상태가 된다.
- [ ] 이슈 쓰기를 다시 시킨다. 기대: 세션 허용이 무효가 되어 카드가 다시 뜨고, 사유에 오염이 표시된다. **거부**한다.
- [ ] 같은 세션에서 `Bash`로 이전에 “세션 허용”한 명령을 다시 시키면 카드가 다시 뜬다(오염 시 에이전트 도구 세션 허용도 무효).
- [ ] 끝나면 `farero-dev/farero-e2e`의 `release check` 이슈를 닫는다.

**D. 앱 꺼짐**
- [ ] 메뉴에서 Farero를 종료한다(데몬은 계속 돈다).
- [ ] 프롬프트: `Call the MCP tool mcp__farero__github_get_me with exactly these arguments: {}` → 카드 없이 실행된다(자동 허용).
- [ ] 프롬프트: `Call the MCP tool mcp__farero__github_create_pull_request with exactly these arguments: {"owner":"farero-dev","repo":"farero-e2e","title":"release check PR","head":"none","base":"main"}`
  - 기대: 자동 거부, 모델이 “앱이 실행 중이 아니라 승인할 수 없음” 이유를 받는다. PR은 만들어지지 않는다.
- [ ] 터미널에서 Bash 권한이 필요한 명령을 시키면 Claude Code 자체 프롬프트로 넘어간다(`PermissionRequest`가 결정 없이 끝남).
- [ ] 앱을 다시 연다(`open /Applications/Farero.app`). 기대: 데몬에 다시 연결되고 세션이 목록에 보인다. 로그에 자동 거부와 “터미널로 넘김”이 남는다.

**E. 터미널 점프**
- [ ] Terminal.app 창 두 개에서 각각 다른 폴더로 `claude` 세션을 열고 아무 프롬프트를 하나씩 보낸다. 세 번째 창(셸만)을 맨 앞에 둔다.
- [ ] 노치를 펼쳐 첫 번째 세션 행을 누른다(또는 행의 **터미널로 이동**).
- [ ] 첫 점프에서 macOS “Farero가 Terminal을 제어하려 함” 자동화 권한 창이 뜬다. **허용**.
- 기대:
  - [ ] 해당 세션의 창·탭이 맨 앞으로 온다.
  - [ ] 두 번째 세션도 같다.
  - [ ] 터미널이 없는 세션(예: tty 없음)이면 “터미널 창을 찾을 수 없음”이 표시된다.
- [ ] 자동화 권한을 거부한 경우를 시험하려면 시스템 설정 › 개인정보 보호 및 보안 › 자동화에서 끄고 다시 눌러 본다. 기대: 오류 안내가 표시되고 앱이 멈추지 않는다.

### 6. 추가 확인

- [ ] 로그 검색…: 한국어 단어(2글자 포함)로 검색해 결과가 나온다.
- [ ] 설정 › 승인 정책에서 도구 단계를 바꿔 본다.
- [ ] 설정 › 일반에서 **업데이트 확인**을 켜고 끌 수 있다. (프리릴리스만 있을 때 “새 버전” 표시가 없는 것은 정상: 업데이트 확인은 `releases/latest`를 보며 프리릴리스를 포함하지 않는다.)
- [ ] 앱을 종료 후 재실행하고, 로그아웃/재부팅 뒤에도 데몬이 올라온다(LaunchAgent 유지).
- [ ] 업데이트 시나리오(가능하면): 이전 버전을 설치한 뒤 새 zip으로 `/Applications`의 앱을 교체해 열면 데몬이 새 버전으로 다시 시작되고 Keychain 확인 창이 다시 뜬다.

### 7. 정리

- [ ] 설정 › Claude Code › **설정 제거…** → **설정 제거**. `settings.json`에 farero 항목이 없고 `claude mcp list`에 `farero`가 없다.
- [ ] 앱 종료 후 README의 Uninstall 절차를 그대로 실행한다.
  - [ ] `launchctl bootout gui/$(id -u)/dev.farero.farerod`
  - [ ] `rm ~/Library/LaunchAgents/dev.farero.farerod.plist`, `rm -rf /Applications/Farero.app`
  - [ ] `rm -rf ~/Library/Application\ Support/Farero ~/Library/Logs/Farero`
  - [ ] Keychain 항목(서비스 `dev.farero.farerod`) 삭제
  - [ ] `defaults delete dev.farero.Farero`
- [ ] 위 절차대로 하면 흔적이 남지 않는지 확인한다(`launchctl list | grep farero`, `ls ~/Library/Application\ Support | grep -i farero`).
- [ ] 외부 자원 정리: `farero-dev/farero-e2e`의 테스트 이슈를 닫고, 연결했던 Railway·GitHub·Resend 앱 승인을 각 서비스의 설정에서 취소한다.
- [ ] 테스트 계정으로 로그아웃하고 원래 계정에서 시스템 설정 › 사용자 및 그룹 › 계정 삭제(홈 폴더 삭제 선택).
- [ ] 결과를 기록한다: 통과/실패 항목, 예상과 달랐던 점(특히 이동 안내, Keychain 창 횟수), 필요한 수정은 이슈로 만든다.
