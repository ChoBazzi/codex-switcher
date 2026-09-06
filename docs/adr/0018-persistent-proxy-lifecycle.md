# ADR 0018: 영속 프록시 수명주기와 제한된 continuation 계약

- 상태: accepted
- 날짜: 2026-09-06
- 관련: ADR 0017, 0009

## 배경과 선택지

저장소만으로는 실제 요청 실패가 재시작 후 차단되지 않는다. 모든 CLI 입력 형태를 추정하여 허용하는 대신, 소유권을 검증할 수 있는 최소 형태부터 프록시에 연결한다.

## 결정

- 선택적 `proxy.NewPersistent`를 추가한다. 기존 `New`와 demo/live-test 동작은 유지한다. 생성 시 저장소가 필수이며 저장 실패를 메모리 경로로 우회하지 않는다.
- 신뢰된 resolver는 인증과 함께 검증된 origin 및 내부 슬롯을 반환한다. 영속 라우터가 이 값을 제공한다. 프록시는 저장소 매핑과 이를 비교하고, upstream 호출 전에 `Begin`으로 소유권 검증과 inflight 기록을 완료한다. 알 수 없는 세션을 등록하지 않는다.
- 초기 요청 계약은 텍스트 입력과 `previous_response_id`이며, ADR 0019에서 소유권이 확인된 item_reference와 함수 호출/문자열 결과를 추가했다. conversation, assistant 전체 이력 및 알 수 없는 확장은 계속 차단한다. 전체 Codex CLI 도구 작업 지원을 의미하지 않는다.
- 정상 JSON 응답의 id/status=completed 또는 SSE response.completed의 response.id/status=completed만 응답 소유권으로 저장한다. 스트림은 completed 뒤 EOF까지 확인한다. ID 누락, 중복 completed, incomplete/failed, 읽기 중단, 클라이언트 쓰기 실패는 실패 처리한다. JSON 관찰은 최대 4 MiB, SSE 이벤트 버퍼는 기존 1 MiB 제한을 따른다.
- 2xx 완료 시 `Finish`를 커밋한 뒤 로컬 요청 상태를 성공으로 바꾼다. 오류/패닉/중단 시 deferred Finish로 blocked를 기록한다. DB 자체가 사용 불가하면 남은 inflight가 재시작 시 blocked로 복구된다. 실패 요청을 다시 전송하지 않는다.
- 응답 바이트를 전달한 뒤 저장 실패가 발생하면 HTTP 상태를 다시 쓰지 않고 연결을 중단한다. 클라이언트가 이미 완료 이벤트를 보았을 수 있으므로 전송 완료와 로컬 커밋의 원자성을 보장하지 않는다. 다음 요청을 차단하여 보수적으로 처리한다.

## 제약과 후속 작업

이 생성자는 내부 연결 경로이며 아직 상주 helper/일반 CLI 런처에서 사용하지 않는다. 신규 세션 신뢰 경계, CLI 도구/item ID 지원, 주기 GC 및 Wiki 인계는 후속 단계다. 지원 범위를 넘어선 실사용을 가능하다고 표시하지 않는다. SQL/인증/원본 본문은 로그로 출력하지 않는다. 외부 의존성 추가 없음.

## 검증

합성 upstream과 SQLite 임시 DB로 JSON/SSE 성공, 재시작 후 소유 continuation, 교차 계정 참조 차단, 429·부분 스트림·잘못된 완료 응답 이후 재시작 재전송 차단을 검증한다.

```sh
go test -race -count=1 ./internal/proxy ./internal/affinity ./internal/routing
```
