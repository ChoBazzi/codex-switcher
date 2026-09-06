# ADR 0020: CLI 영속 프록시 함수 왕복과 입력 ID 등록

- 상태: accepted
- 날짜: 2026-09-06
- 관련: ADR 0017, 0018, 0019

## 배경

직접 합성 서버로 보내던 CLI 함수 테스트를 영속 프록시 경유로 확장했다. 설치된 CLI는 요청에 include/prompt_cache_key/client_metadata를 넣고, 새 텍스트 메시지와 함수 결과에도 비어 있지 않은 클라이언트 ID를 붙였다. 이러한 입력을 모두 기존 참조로 간주하면 첫 요청 또는 함수 결과가 차단된다.

## 결정

- 확인된 세 메타데이터 필드를 허용하지만 세션·계정 선택의 근거로 사용하지 않는다.
- 전체 내용이 제공된 user/developer/system 텍스트 메시지와 소유 call_id가 검증된 문자열 함수 결과의 새 ID만 `item:<id>`로 등록한다. ID만 있는 item 참조나 assistant 메시지를 신규 입력으로 등록하지 않는다.
- `BeginWithInputs`는 기존 참조 소유권을 **먼저** 검증하고, 새 입력 ID와 inflight 의도를 같은 트랜잭션에 기록한다. 다른 세션의 소유권은 덮어쓰지 않으며 충돌 시 모두 롤백한다. 새 ID를 자기 참조로 동시에 제출해 검사를 우회할 수 없다.
- 소유권 검사는 본문의 출처/내용 진위를 검증하는 기능이 아니다. 입력 본문은 저장하지 않는다. 이미 관찰한 동일 ID가 다른 세션에서 등장하면 보수적으로 차단한다.
- 테스트는 실행한 격리 CLI의 `thread.started` 이벤트로만 세션을 등록한다. HTTP 헤더의 ID는 등록된 ID와 일치해야 한다. 이벤트가 없으면 제한 시간 후 거절한다. 헤더만 보고 자동 등록하지 않는다.

## 검증과 한계

`TestInstalledCodexPersistentFunction`은 CLI → 영속 프록시 → 합성 upstream → 존재하지 않는 함수 호출 → CLI 오류 결과 반환 → 영속 프록시 → 최종 응답의 왕복 성공을 확인했다. 실제 모델이나 실제 도구는 실행하지 않는다. ID 값과 본문 대신 합성 필드 이름만 진단 출력한다.

실패 시 `probe diagnostics`에 시작 이벤트 확인 여부, 등록/스캐너 오류 여부, 시간 초과, CLI 종료 코드, 프록시 요청 횟수/마지막 HTTP 상태, upstream 횟수 및 함수 결과 인식 여부를 출력한다. 원본 CLI 출력과 식별자 값은 출력하지 않는다.

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v ./internal/cliprobe -run TestInstalledCodexPersistentFunction
```

이 경로는 합성 모델 프로필로 검증한 개발 테스트다. 기본 모델의 additional_tools, reasoning/assistant 전체 이력, 실제 도구 실행, 일반 CLI 런처와 같은 TUI 프로세스 내 전환은 아직 지원 검증 대상이다. 기존 demo/live-test는 변경하지 않는다. 외부 의존성 추가 없음.

ADR 0022 연결 이후 전체 회귀 테스트를 재실행하여 통과했다. 새 ID 충돌·롤백·자기 참조 방지와 파서 검사는 포트를 열지 않는 로컬 테스트로도 검증한다.

## 근거

이전 작업에서 OpenAI Docs의 [비대화형 CLI 문서](https://learn.chatgpt.com/docs/non-interactive-mode)를 확인하고, 실제 설치 CLI의 이벤트와 필드 형식을 합성 환경에서 검증했다.
