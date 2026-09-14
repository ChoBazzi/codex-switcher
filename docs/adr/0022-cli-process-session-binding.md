# ADR 0022: CLI 프로세스별 인증과 세션 연결

- 상태: accepted
- 날짜: 2026-09-06
- 관련: ADR 0009, 0020

## 결정

테스트 안에 있던 시작 이벤트 등록 절차를 `internal/clisession`으로 옮겨 영속 라우터와 연결한다. HTTP 요청의 대화 ID만으로 세션을 생성하는 방식은 채택하지 않는다.

- Binding 하나는 helper가 실행한 CLI 프로세스 하나에 대응한다. 신뢰된 프로젝트/worktree/브랜치 메타데이터, 실행별 비밀값과 명시적 사용자 입력 확인으로 생성한다. 호출자는 충분히 무작위인 실행별 비밀값을 생성해야 하며 테스트만 고정 합성 값을 사용한다.
- `Started`는 해당 CLI의 stdout에서 확인한 thread.started ID를 받는다. 새 대화는 사용량 정책에 따라 등록하며, resume은 명시한 기존 ID와 origin이 일치하고 사용 가능한 경우에만 연결한다. 알 수 없는 resume은 신규 등록으로 대체하지 않는다.
- 시작 이벤트 처리 실패는 해당 Binding에서 끝난다. 다른 ID로 재등록을 시도하지 않으며 한 프로세스의 두 번째 시작 이벤트는 거절한다.
- 요청은 X-Switcher-Run 비밀값을 정확히 한 개 제공해야 한다. 이후 기존 Thread-Id/Session-Id 검증과 등록된 ID 일치를 확인한다. 시작 이벤트를 최대 2초 기다리고 취소 또는 종료 시 거절한다. Close 이후 새 요청을 수락하지 않는다.
- 요청마다 영속 라우터를 통해 사용량·인증·차단 상태를 재확인한다. 계정 재선택, upstream 재시도, Wiki 자동 시작은 하지 않는다.
- 테스트는 synthetic 인증·사용량으로 실제 설치 CLI를 이 모듈 → 영속 라우터 → 영속 프록시에 연결한다. 실제 계정이나 모델, 도구를 호출하지 않는다.

## 검증

인증 누락, 시작 이벤트 누락, 다른 thread ID, 두 번째 시작, 종료 후 요청, 알 수 없는 resume, 다른 worktree 및 사용자 입력 누락을 검사한다. 설치된 CLI 함수 왕복 테스트도 새 모듈을 사용한다.

```sh
go test -race -count=1 ./internal/clisession
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 ./internal/cliprobe -run TestInstalledCodexPersistentFunction
```

일반 CLI 실행 명령과 프로세스 관리자, 실제 프로젝트 메타데이터 산출, 주기 사용량 조회 연결은 후속 작업이다. 이 모듈만으로 일반 TUI의 계정 전환을 제공하지 않는다. 외부 의존성 추가 없음.
