# 0011. Phase 0 로컬 검증 기반 구현

- 상태: accepted
- 날짜: 2026-09-06

## 결정 배경

실제 계정의 비공개 프로토콜에 의존하기 전에 로컬 검증·인계·무재시도 경계를 합성 데이터로 검증한다.

## 선택과 범위

- Go 1.24 이상 표준 라이브러리로 시작한다. 외부 의존성 없이 `os.Root`의 경로 경계를 사용한다.
- 체크포인트 파일 기본 상한은 32 KiB. 작성·검증 시간은 합의한 180초이며 시간 제어는 결정론적으로 테스트한다.
- Markdown 첫 줄은 `<!-- codex-switcher:{JSON metadata} -->`, 필수 섹션은 `Goal`, `Changes`, `Decisions`, `TODO`, 마지막은 `<!-- checkpoint-complete:ID -->`다. 이는 형식 검증이며 의미적 정확성 보증은 아니다.
- 완성 파일은 임시 파일에서 원자적으로 rename하여 게시해야 한다. 최종 경로의 symlink를 거절하고 검증한 바이트를 별도 스냅샷으로 고정한다. 완료 표지만으로 원자적 게시를 증명할 수 없으므로 실제 Codex 작성 방식은 후속 검증 대상이다.
- Phase 0 상태와 스냅샷은 메모리 기반이다. 아직 SQLite 영속화·전역 롤링 백업·실제 OAuth·CLI 지시 주입이 구현됐다고 간주하지 않는다.
- 인계 저장소는 명시적 예약을 원자적으로 한 번만 새 대화에 연결한다. 인계 없는 일반 세션은 예약을 소비하지 않는다. 사용자 입력·작업 경계 정보는 검증된 내부 호출자가 공급해야 하며 클라이언트의 임의 boolean을 신뢰하는 HTTP API는 제공하지 않는다.

## 검증

`go test -race ./...`로 ID 불일치, 다른 세션, 부분 파일, symlink, 시간 초과, 명시적 재확인 및 스냅샷 불변성을 확인한다. 실제 계정·대화·인증 자료는 사용하지 않는다.

## 단일 시도 프록시

- 고정 upstream의 POST 요청만 중계하고 헤더를 새로 구성하여 inbound 인증·계정·cookie·idempotency 정보를 전달하지 않는다.
- `RoundTrip` 한 번, HTTP/1 새 연결, redirect 미추적, `GetBody` 제거로 transport 재생 가능성을 줄인다. 동일 세션은 한 번에 하나의 요청만 처리하고 실패하면 차단한다. 사용자 새 입력과 CLI 내장 재시도 식별은 실제 어댑터 검증 전 보장하지 않는다.
- SSE 바이트는 그대로 전달하며 terminal event를 관측한다. 실패·미완료·완료 없는 EOF는 연결을 중단한다. 이미 시작된 HTTP 상태를 오류 JSON으로 바꾸지 않는다.
- 실행 바이너리는 `--demo`로 합성 upstream만 제공한다. 실제 계정·OAuth·사용량 조회·Wiki 지시 주입·Control API의 전체 구현을 노출하지 않는다. `X-Switcher-Demo-Session`은 데모 전용이며 공식 Codex 헤더라고 가정하지 않는다.
- SSE 관측 최대 이벤트 크기는 1 MiB, 요청 본문은 4 MiB, upstream 헤더 대기는 30초, 전체 요청은 10분으로 시작한다. 실제 프로토콜 검증에서 조정한다.
- TLS/HTTP 정책 참고: [Go HTTP Transport](https://pkg.go.dev/net/http#Transport), [Responses streaming](https://developers.openai.com/api/docs/guides/streaming-responses).
