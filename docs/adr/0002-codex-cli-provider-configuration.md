# 0002. Codex CLI 프록시 연동 방식 (Profile 기반)

- 상태: accepted
- 날짜: 2026-09-05

## 결정 배경

Codex CLI의 모든 요청을 로컬 역방향 프록시(`127.0.0.1:<port>`)로 전달하여 인증 헤더 교체 및 계정 라우팅을 수행해야 한다. 
CLI가 프록시를 바라보게 만드는 방법에는 전역 설정 파일(`config.toml`) 수정, 환경 변수 주입, CLI 프로필 기능, 네트워크 프록시 등이 존재한다.

## 검토한 선택지

1. **전역 설정 파일 직접 수정 (`~/.codex/config.toml`)**:
   - `chatgpt_base_url`을 프록시 주소로 직접 수정.
   - 장점: 일반 `codex` 실행만으로 프록시 적용.
   - 단점: 프록시 데몬 미실행 시 CLI 연결 오류 발생, 사용자 원본 설정 덮어쓰기 리스크.
2. **전용 프로필 활용 (`codex -p switcher`, 선택)**:
   - `$CODEX_HOME/switcher.config.toml`을 생성하고, 사용자는 `codex -p switcher`로 실행.
   - 장점: 사용자의 기본 `config.toml`을 전혀 건드리지 않아 안전함. 비상 시 순정 `codex`로 즉시 복구 가능. 공식 계층형 설정 기능 활용.
   - 단점: 실행 시 `-p switcher` 인자가 필요함 (메뉴바 앱에서 `alias codex='codex -p switcher'` 셸 연동 옵션을 제공하여 해결).
3. **환경 변수 주입 (`CHATGPT_BASE_URL` 등)**:
   - 터미널 탭마다 환경 변수 누락 가능성 및 GUI 앱 연동 한계.
4. **네트워크 프록시 (`HTTP_PROXY`)**:
   - HTTPS MITM을 위한 로컬 루트 CA 인증서 생성 및 신뢰 등록 필요(보안 및 복잡도 문제로 기각).

## 최종 결정

**2번: 전용 프로필(`$CODEX_HOME/switcher.config.toml` 및 `codex -p switcher`) 방식**을 채택한다.

- 메뉴바 앱/Go 헬퍼가 `$CODEX_HOME/switcher.config.toml`에 다음 설정을 작성/관리한다.
  ```toml
  chatgpt_base_url = "http://127.0.0.1:<port>/backend-api/"
  ```
- 원본 `~/.codex/config.toml`은 변경하지 않는다.
- 사용자가 평소처럼 `codex`로 실행할 수 있도록, 메뉴바 앱에서 `alias codex='codex -p switcher'` 등록 기능을 제공한다.

## 예상되는 결과와 장단점

### 장점
- 원본 설정 파일이 100% 안전하게 보존된다.
- 프록시 데몬이 꺼져 있거나 문제가 생겨도 사용자는 `\codex` 또는 인자 없는 `codex`로 순정 CLI를 즉시 사용할 수 있다.
- 공식 기능(`--profile`)을 이용하므로 CLI 버전 업데이트에 안정적이다.

### 단점
- 사용자가 alias를 설정하지 않은 환경에서는 명시적으로 `-p switcher`를 붙여야 한다.
