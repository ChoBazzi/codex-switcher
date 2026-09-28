# ADR 0071: 명시적 사용량 소진 후 같은 CLI 요청의 계정 전환

- 상태: accepted
- 날짜: 2026-09-22
- 관련: ADR 0042, 0043, 0049, 0055, 0058, 0061, 0070

## 배경과 선택지

긴 도구 작업에서는 turnPending이 유지되어 일반 5% 자동 선택이 생략된다. 도중 한도가 소진되면 기존 구현은 실패 잠금을 걸어 다음 입력도 자동 선택 전에 차단한다. 사용자는 같은 CLI 작업을 다른 계정에서 계속 처리하도록 요청하고, 명확한 리밋과 일반 오류를 구분해 자동 전환하는 예외를 승인했다.

모든 429를 재시도하면 일시적인 요청 속도 제한까지 계정 전환으로 대응하게 된다. 메시지 문자열 매칭은 언어·문구 변화와 임의 텍스트 때문에 채택하지 않는다. 출력이 전달된 스트림을 새 응답과 합치면 도구 중복 실행과 문맥 불일치가 생긴다. 따라서 명시적 소진과 출력 전 응답만 제한적으로 처리한다. 새 세션 인계나 자동 요약 모델 요청은 추가하지 않는다.

## 결정

- managed `--auto --tools`의 루트와 보조 대화만 사용량 소진 분류를 활성화한다. 저수준 proxy handler와 다른 기존 실행 경로는 계속 단일 시도다. 루트·보조 각각의 coordinator만 재전송 예외를 소유한다.
- HTTP 429의 정상 JSON `error.type` 또는 `error.code`가 정확히 `usage_limit_reached`인 경우를 인정한다. 충돌하는 type/code는 거절한다. 코드 없는 429, `rate_limit_exceeded`, `insufficient_quota`, message 문자열, 인증·네트워크·일반 서버 오류는 인정하지 않는다.
- HTTP 200 SSE의 첫 의미 있는 이벤트가 `error` 또는 `response.failed`이며 동일 명시적 코드를 담은 경우도 인정한다. 앞에 내용 없는 `response.created`/`response.in_progress`와 comment만 허용한다. 텍스트·reasoning·도구 등 어떤 출력 이벤트라도 먼저 나오면 재전송하지 않는다. 오류 응답은 최대 64 KiB만 검사하고 HTTP 429 본문 읽기는 3초로 제한한다. 부분·손상·초과 응답은 소진으로 추정하지 않는다. 검사로 읽은 일반 응답 바이트는 원래 순서로 복원한다.
- 인정된 소진 시도는 CLI에 헤더·본문을 보내지 않는다. 최신의 유효 잔여량이 5% 초과인 대안 중 하나를 선택한다. 요청별로 시도한 슬롯을 제외해 한 슬롯 최대 한 번, 최대 5개 슬롯까지만 시도한다. 다음 계정의 일반 실패는 즉시 종료하고 재시도하지 않는다. 대안이 없으면 고정된 사용량 소진 오류를 반환하고 기존 실패 잠금을 적용한다.
- 모델의 명시적 소진 관측은 이전에 시작된 사용량 조회보다 우선한다. 공유 메모리 overlay에서 슬롯별 거절 시각만 보관해 늦게 완료된 기존 조회가 소진 계정을 다시 선택 가능하게 만들지 않는다. 거절 이후 시작한 정상 사용량 관측은 기존 정책에 따라 다시 반영한다. 이 overlay는 재시작 시 복원하지 않는다.
- 기존 본문이 계정 소유권·도구 호출/결과 검증을 통과한 뒤에만 fallback을 준비한다. 원래 입력에서 모든 reasoning을 제거하고 완료된 function/custom 호출·결과의 call_id를 대상 슬롯별로 재구성하며 item ID를 제거한다. 텍스트·호출 인자·도구 결과는 유지한다. 호출 누락·중복·고아 결과·서버 continuation·암호화 compaction은 통과시키지 않는다. 도구 실행 자체나 추가 요약 요청은 하지 않는다.
- 전환된 사용자 경계를 메모리에 표시한다. 동일 사용자 턴의 다음 입력도 이전 계정의 reasoning과 식별자가 다시 들어가지 않도록 같은 변환을 적용한다. 새 사용자 턴은 기존 정규화로 돌아간다. restart는 기존 checkpoint의 새 사용자 입력 조건을 유지하므로 portable 모드를 복원해 중단 요청을 재실행하지 않는다.
- 전환 슬롯과 busy 상태(보조는 Thread/Root/Slot 바인딩)를 다음 upstream dispatch 전에 checkpoint에 저장한다. 저장 실패는 새 dispatch를 막는다. 대상 인증을 확인하고 실제 dispatch의 소유권도 검증해 정규화 중 인증 교체를 통과시키지 않는다. 취소 후 새로운 dispatch는 금지한다.
- 보조 대화는 명시적 소진 때 자기 계정만 변경한다. 루트·형제의 handler, 이력 소유권 또는 실패 잠금을 빌리지 않는다. 루트의 제한된 fallback도 보조 작업을 중단하지 않는다. 활동 잠금은 전환 사이에도 유지해 로그인·로그아웃과 겹치지 않는다.
- 네이티브 암호화 압축 이력은 옮기지 않는다. 이미 출력한 요청·일반 오류·서버 코드가 불명확한 경우에는 기존 실패 처리와 명시적 복구를 유지한다. UI에 사용량 소진으로 전환을 완료하지 못한 경우를 일반 rate-limit 진단과 구분해 표시한다.

## 한계

2026-09-23: [ADR 0072](0072-agent-message-encrypted-attachments.md)에 따라 암호화 협업 지시와 해당 협업 도구 호출 인자가 포함된 이력은 portable 정규화에서 거절한다. 실제 작업 지시를 삭제하고 전환하는 것을 방지한다.

`usage_limit_reached`는 허용 목록이며 실제 사용자의 오류 본문을 수집·보관하지 않았다. 서버가 다른 코드를 사용하면 자동 전환하지 않는다. 합성 검사는 서비스의 실제 계정 간 전체 대화 수락이나 모델 작업 결과의 동일성을 보장하지 않는다. reasoning 제거는 모델의 추론 연속성·캐시에 영향을 줄 수 있다. 이미 실행한 도구 결과는 유지하지만 모델이 새로운 호출로 같은 작업을 다시 제안하는 것까지 프록시가 의미적으로 판별하지는 않는다.

5% 정책의 선제 전환 시점 자체는 이번에 넓히지 않는다. 정상 도구 왕복은 기존 계정을 유지하고 명시적 소진 응답 때만 이 예외를 적용한다. 기능을 넓혀 일반 오류를 재시도하거나 이미 출력된 응답을 자동 연결하지 않는다. 추가 의존성 없음.

## 검증

- `TestUsageLimitClassification`, `TestUsageLimitReadFailureNeverQualifies`: JSON/SSE 허용 코드, generic429·충돌 코드·손상·과대·부분 출력·도구 출력·읽기 오류 및 원본 바이트 보존.
- `TestUsageLimitDispatch`: 포트 없는 메모리 HTTP transport로 실제 proxy dispatch에서 명시적 소진 출력 보류, 일반 오류와 부분 스트림의 비전환, 취소 무전송 및 handler 자체 무재시도.
- `TestQuotaPortableHistory`, `TestQuotaRejectionOverridesInflightObservation`: 도구 결과·사용자 입력 보존, reasoning/ID 제거, 불완전 도구·compaction·서버 참조 차단, 늦은 사용량 관측 격리.
- `TestProbeUsageLimitFailover`: 실제 loopback에서 연결 1·2의 긴 도구 턴 A→B, JSON/SSE 오류 은닉, B의 다음 도구 후속 유지, 일반429·부분 출력 무전환, 대안 소진 시 종료.
- `TestProbeQuotaBoundaryReproduction`: bare429는 계속 무재전송이며 완료된 턴의 일반 5% 선택은 유지되는 음성 대조.
- `TestAuxiliaryUsageLimitFailover`: 보조 대화의 독립 A→B 전환과 같은 턴의 후속 도구 이력 정규화.
- `TestServiceForwardedQuotaDiagnostic`: coordinator에서 정규화한 한도 진단을 relay가 다시 검증해 캐시에 보관하고, 임의 코드·날짜·scope·추가 비밀 필드를 전달하지 않는다. 기존 다중 연결 경로에서 이미 정규화한 진단이 relay 허용 목록에 없어 유실되던 경로도 수정했다.

2026-09-22 검증 결과: 포트 없는 오류 분류·실제 dispatch·이력 정규화·한도 overlay·진단 relay 검사와 기존 선택/턴 경계 검사를 `go test -race`로 통과했다. 전체 패키지와 테스트 컴파일(`go test ./... -run '^$'`), `go vet ./...`, Xcode 도구 모음의 Swift 빌드 및 `DirectProxyCheck`를 통과했다. 실제 loopback 기반 신규 root/auxiliary 통합 테스트는 작성했으나 실행하지 못했다. 권한 확대 실행의 자동 승인 검토가 `409 probe_previous_request_failed`로 실패하여 테스트 명령 자체가 실행되지 않았다. 위험하다는 심사 판정과 구분한다. 전체 `verify.sh`, 설치 CLI를 통한 이번 자동 전환, 실계정 수락은 아직 미검증이며 실행 중인 사용자 daemon은 교체하지 않았다.

2026-09-22 후속 검증: 쓰기 권한이 허용된 세션에서 loopback 바인딩의 샌드박스 제한을 확인한 뒤 권한 확대 승인을 받아 `TestProbeUsageLimitFailover`, `TestAuxiliaryUsageLimitFailover`, `TestProbeQuotaBoundaryReproduction`을 `go test -race -count=1 -timeout 90s`로 모두 통과했다. 이전의 `409 probe_previous_request_failed` 승인 검토 오류는 재발하지 않았다. 이어서 `DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer sh macos/Checks/verify.sh` 전체를 통과했다. Go race/vet, 설치 CLI 합성 통합 검사, helper·Swift 빌드, 모델·제어 서버·로그인·DirectProxy 검사와 밝은/어두운 UI 검사를 완료했다. 실패 수정은 필요하지 않았으며 기존 Swift 캡처 및 deprecated API 경고는 남아 있다. 실행 중인 사용자 앱·daemon은 종료하거나 교체하지 않았다. 설치 CLI 검사는 기존 합성 호환성 검사이며, 이번 자동 전환의 설치 CLI 경유 검증과 실계정 수락 검증은 아직 수행하지 않았다.

적용 시 기존 작업 종료 후 새 helper와 앱을 사용해야 하며, 현재 실패 상태를 코드 빌드만으로 자동 해제하거나 재실행하지 않는다.
