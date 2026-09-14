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
- 정상 JSON 응답의 id/status=completed 또는 SSE response.completed의 response.id/status=completed만 응답 소유권으로 저장한다. 스트림은 completed 뒤 EOF까지 확인한다. ID 누락, 중복 completed, incomplete/failed, 읽기 중단, 커밋 전 클라이언트 쓰기 실패는 실패 처리한다. JSON 관찰은 최대 4 MiB, SSE 이벤트 및 완료 보류 버퍼는 1 MiB 제한을 따른다.
- 2xx 완료 시 `Finish`를 커밋한 뒤 로컬 요청 상태를 성공으로 바꾼다. 오류/패닉/중단 시 deferred Finish로 blocked를 기록한다. DB 자체가 사용 불가하면 남은 inflight가 재시작 시 blocked로 복구된다. 실패 요청을 다시 전송하지 않는다.
- 일반 SSE 이벤트는 프레임 단위로 원본 바이트를 전달하되 완료 이벤트와 그 뒤의 바이트는 보류한다. EOF와 마지막 이벤트 검증, SQLite 커밋 후에만 완료 이벤트를 전달한다. 정상 JSON은 본문 전체를 검증/커밋 후 전달한다. 저장 실패 시 완료를 전달하지 않고 연결을 중단한다.
- 서버 완료를 검증하고 커밋한 뒤의 클라이언트 전달 실패는 영속 성공을 되돌리지 않는다. 사용자에게 전달됐다는 보장은 아니며 자동 재전송도 하지 않는다. CLI 실행기의 별도 정상 종료 확인이 실패하면 해당 CLI 기록은 재개 불가 상태를 유지한다.

## 완료 전달 순서 보완 (2026-09-06)

실사용에서 HTTP 200 및 CLI 정상 종료 후 `affinity_requires_user_action`이 보고됐다. 기존 전달 순서는 완료 이벤트 수신 직후 CLI가 연결을 닫으면 EOF/커밋 전에 실패로 차단될 수 있다. 지연 EOF와 완료 수신 시 쓰기 실패를 사용하는 합성 회귀 테스트에서 이 경합을 재현했다. 위 완료 보류 정책으로 해결하며 기존 차단 세션은 해제하지 않는다. 원래 실계정 실패의 모든 내부 원인을 재현했다는 의미는 아니다.

CRLF·조각난 이벤트·중복 완료·완료 후 실패·버퍼 초과를 검증한다. upstream EOF가 지연되면 CLI의 최종 완료 표시도 지연되며, 기존 타임아웃을 넘기면 차단한다. 재요청으로 복구하지 않는다.

## 제약과 후속 작업

이 생성자는 내부 연결 경로이며 아직 상주 helper/일반 CLI 런처에서 사용하지 않는다. 신규 세션 신뢰 경계, CLI 도구/item ID 지원, 주기 GC 및 Wiki 인계는 후속 단계다. 지원 범위를 넘어선 실사용을 가능하다고 표시하지 않는다. SQL/인증/원본 본문은 로그로 출력하지 않는다. 외부 의존성 추가 없음.

## 검증

### 스트리밍 출력의 이력 등록 (2026-09-07)

기존 구현은 response.completed.output만 등록하여 앞선 response.output_item.done에만 존재하는 출력의 소유권을 누락했다. 최종 output이 빈 합성 응답과 실제 CLI 재개로 기존 409 continuation_or_session_unavailable을 재현했다. strict 관찰기는 output_item.done도 동일한 출력 계약으로 검증하고 참조를 누적하며, completed의 참조와 중복 제거 후 EOF·DB 커밋 검증을 거쳐 함께 저장한다. item 이벤트만으로 성공 처리하거나 DB에 미리 저장하지 않는다. 완료 후 item, 오류 이벤트 및 누적 item 원문 1 MiB 초과는 차단한다. 사용자 원문은 로그/fixture/DB에 추가 저장하지 않는다.

message/reasoning의 historyKey 실패를 조용히 무시하던 동작은 completion_history_invalid로 명시적으로 거절한다. 실패 요청의 재전송 및 기존 재개 불가 기록 복구는 하지 않는다. 보고된 실계정 응답 원문은 보관하지 않아 실제 서버가 마지막 output을 비웠는지까지 직접 확인한 것은 아니며, 실계정 확인은 새 대화로 수행한다.

### 완료 형식 진단 보완 (2026-09-07)

후속 실계정 진단에서 Content-Type 누락과 JSON 파싱 실패를 확인했다. 성공한 영속 응답에 헤더가 없는 경우에만 최대 6바이트 접두사(`data:`, `event:`, SSE 주석 `:`)로 SSE 경로를 선택하며 `response_format=missing_sse`로 표시한다. 버퍼링한 원본 바이트와 읽기 오류는 그대로 파서에 전달한다. 명시된 Content-Type은 덮어 추측하지 않는다. 접두사는 성공 판정이 아니며 기존 완료 이벤트·EOF·소유권 커밋 검증이 모두 필요하다. HTML, 부분 응답, 오류 이벤트는 성공으로 처리하지 않는다. 실계정 본문이 실제 SSE였는지는 다음 실행으로 확인해야 한다.

`response_format`은 Content-Type을 표준 미디어 타입 파서로 판별한 로컬 상수(sse/json/other/missing/invalid)만 기록한다. 기존 접두사 비교와 달리 선행 공백과 대소문자를 처리하고 `text/event-stream-invalid` 같은 다른 타입을 SSE로 오인하지 않는다. 헤더가 JSON이면 SSE를 추측하거나 본문을 변환하지 않는다.

`completion_shape_invalid`의 세부 원인은 `completion_rejection_code`로 구분한다. JSON 문법/중복 키, 객체 형태, error 존재, status, 응답 ID, output 및 item/함수 필드를 값 없이 분류한다. non-null error가 있는 응답은 completed 상태라도 소유권을 커밋하지 않는다. 원본 오류·필드 값은 출력하지 않는다. 정상 판정 조건을 느슨하게 하거나 실패 요청을 재전송하지 않는다. 실계정에서 보고된 기존 한 줄만으로 특정 필드가 원인이라고 단정할 수 없으므로 다음 사용자 실행의 세부 진단으로 확인한다.

영속 2xx 응답 경로는 `response_failure_code`, `completion_seen`, `completion_committed`를 종료 진단에 제공한다. 서버 failed/incomplete/error 이벤트, 완료 형식 오류/중복/누락, 잘린 마지막 프레임, 버퍼 초과, 읽기 취소/타임아웃/실패, 클라이언트 쓰기/flush 실패, 저장 충돌/실패를 로컬 상수로 구분한다. 완료 이벤트 관찰은 유효성 검사 성공을 의미하지 않는다. 커밋 후 전달 실패는 committed=true와 별도 오류로 표시하며 성공 기록을 되돌리지 않는다. 원본 오류 code/message, SQL 오류, 계정 ID 및 본문은 출력하지 않는다. 이 진단 추가는 재시도/전달 순서/허용 형식을 변경하지 않는다.

합성 upstream과 SQLite 임시 DB로 JSON/SSE 성공, 재시작 후 소유 continuation, 교차 계정 참조 차단, 429·부분 스트림·잘못된 완료 응답 이후 재시작 재전송 차단을 검증한다.

```sh
go test -race -count=1 ./internal/proxy ./internal/affinity ./internal/routing
```
