# ADR 0038: 직접 실행한 Codex CLI의 프로필·훅 연결

- 상태: implemented (직접 연결 첫 단계, 실제 TUI 승인 후 검증 대기)
- 날짜: 2026-09-08
- 관련: ADR 0002, 0022, 0037

## 실행 구조

사용자는 `switcher-helper direct-proxy -C PROJECT`로 모델 프록시만 실행하고, 별도 터미널의 같은 프로젝트에서 `codex -p switcher`를 직접 실행한다. 서버는 Codex 자식 프로세스를 만들지 않고 입력창·권한 설정·슬래시 명령을 대체하지 않는다. 계정은 기존 Keychain 등록 정보를 사용한다. 모델 provider의 endpoint/인증/재시도만 전용 profile에 설정하고 원본 config.toml, 로그인과 모델·sandbox·approval 설정은 변경하지 않는다.

프로필은 기존 CODEX_HOME(없으면 ~/.codex)에 `switcher.config.toml`과 `switcher-connection.json`을 0600/O_EXCL로 생성한다. 기존 파일이 있으면 덮어쓰지 않고 시작을 거절한다. 정상 종료 시 생성 당시 내용이 그대로인 파일만 제거한다. 강제 종료 시 남은 파일은 자동 덮어쓰지 않는다. 파일의 비밀값을 공유하거나 저장소에 추가하면 안 된다. 서버는 임의의 loopback 포트를 사용하며 서버 재시작 뒤에는 CLI도 새 profile을 읽도록 재시작해야 한다.

## 신뢰와 세션 등록

공식 [Hooks 문서](https://learn.chatgpt.com/docs/hooks)의 SessionStart/UserPromptSubmit/SessionEnd 입력을 사용한다. profile의 inline hooks가 helper의 `direct-hook` 명령을 호출한다. 훅 명령은 프롬프트·transcript 경로를 버리고 이벤트명·session ID·cwd·시작 유형만 로컬 제어 API에 전달한다. 훅 반환에는 원문 오류나 비밀값을 포함하지 않는다.

훅은 사용자가 Codex `/hooks`에서 검토·승인해야 한다. 자동 trust, trust bypass, 관리형 hook 설치는 하지 않는다. 처음 시작할 때 건너뛴 훅은 승인 뒤 `/new`로 새 세션을 생성해야 한다. 훅이 실행되지 않거나 실패하면 프록시는 미등록 세션을 거절한다.

- SessionStart의 startup/clear는 신규 후보를 기록할 뿐 계정 배정이나 모델 호출을 하지 않는다.
- UserPromptSubmit에서만 신규 후보를 기존 라우터에 등록한다. 기존 세션은 원래 계정으로 Resolve한다.
- resume/compact는 기존 소유권이 있는 세션만 허용한다. 모르는 resume을 신규 세션으로 바꾸지 않는다.
- 모델 요청 헤더의 ID만으로 등록하지 않는다. 훅으로 확인한 원본과 요청 시 다시 계산한 프로젝트/worktree/브랜치가 일치해야 한다.
- SessionEnd는 메모리의 활성 연결을 제거한다. 영속 계정 소유권은 보존한다.

모델 프록시용 비밀값과 훅용 비밀값은 별개다. 훅 클라이언트는 loopback만 허용하고 HTTP proxy 환경 변수·redirect·자동 retry를 사용하지 않는다. 직접 연결은 같은 OS 사용자와 신뢰한 로컬 hook의 경계이며 동일 사용자 악성 프로세스에 대한 별도 격리를 제공하지 않는다.

## 현재 제약

- 서버 한 개는 지정한 프로젝트/worktree/브랜치에 한정한다. 메모리 세션 수는 64개로 제한한다.
- 기존 affinity 저장소의 단일 소유 잠금을 유지한다. 서버가 실행 중이면 기존 helper exec/Wiki 인계/로그아웃과 잠금 충돌할 수 있다. 아직 앱의 전환 버튼과 연결하지 않았다.
- 기존 상태 조회 서버와 별개이며 앱 자동 시작과 native CLI 세션 카드는 후속이다. native CLI 기록을 exec 전용 clirecord인 것처럼 등록하지 않는다.
- 파일 수정 권한은 원래 Codex 설정을 따른다. 프록시가 모든 도구·첨부·압축 형식을 지원한다고 보장하지 않는다. 미지원 형식과 실패 요청은 차단하고 재전송하지 않는다.
- 같은 TUI에서 다른 계정의 새 세션으로 Wiki 인계하는 기능은 이번 구현에 포함하지 않는다.
- 서버 stdout의 direct_proxy_request_finished는 누적 프록시 진단이다. 동시 요청별 독립 결과나 CLI 턴 성공 판정으로 해석하지 않는다. 원문·토큰·실제 계정 ID는 출력하지 않는다.

## 검증

실제 서버용 인증값은 crypto/rand의 32바이트를 base64url로 인코딩한다(256비트, 43자). 초기 rand.Text의 26자 결과가 bridge의 최소 32자 검사에 걸리던 시작 오류를 수정했다. 고정 fixture뿐 아니라 실제 생성 함수 → bridge 생성 → 임시 profile 설치 경로를 회귀 검사한다. 검증 기준을 낮추거나 인증을 우회하지 않는다.

Go race 검사: 사용자 입력 전 무호출, 알 수 없는 resume, 프로젝트 불일치, 비밀값 분리, 입력 비저장, 기존 파일 보존, redirect/retry 금지. 인증된 실제 HTTP 훅 → 영속 프록시 → 합성 upstream 및 재개 경로를 검사한다.

설치된 CLI가 생성 profile을 읽고 미승인 훅 상태에서 upstream으로 전송하지 않음을 별도 opt-in 검사한다. 실제 TUI의 훅 승인 후 실계정 대화는 사용자 검증 단계다. 테스트 외 제품 코드는 Codex 프로세스를 실행하지 않는다.
