# ADR 0067: 인증 대기 취소와 시작한 토큰 갱신의 안전한 완료

- 상태: accepted
- 날짜: 2026-09-21
- 관련: ADR 0053, 0064, 0065

## 배경과 선택지

CLI 연결이 끊기거나 요청이 취소되어도 인증 resolver는 mutation 잠금에서 OAuth 완료까지 기다렸다. 반대로 CLI의 context로 이미 시작한 OAuth까지 취소하면 서버에서 회전한 refresh token을 저장하지 못할 수 있다.

대기 goroutine을 따로 남겨 즉시 반환하는 방법은 취소된 요청이 나중에 갱신을 시작할 위험이 있다. 따라서 잠금 대기 자체를 취소 가능하게 하고, 영속 갱신 의도를 저장하기 시작한 뒤에는 기존 단일 교환과 저장을 마친다.

## 결정

- `RequestAccessContext`에 루트·보조 HTTP 요청 context와 사용량 조회 context를 전달한다. 기존 `RequestAccess`는 background context를 사용하는 호환 진입점으로 유지한다.
- mutation 잠금은 용량 1의 channel을 사용하며 대기 중 취소되면 인증 결과 없이 반환한다. 로그인·로그아웃과의 직렬화, 프로세스 간 계정 작업 잠금 및 재조회는 유지한다. 취소된 대기자를 위한 별도 goroutine이나 자동 재시도는 만들지 않는다.
- 진입 시점, 잠금 획득 후, `refresh_blocked` 저장 직전에 취소를 확인한다. 영속 의도 저장이 시작되면 CLI 취소와 독립적인 기존 15초 OAuth 제한 아래 한 번만 교환한다. 신원 검증과 토큰·이력 소유권 저장까지 mutation 잠금 및 프로세스 간 잠금을 유지한다.
- 갱신을 시작한 요청은 취소되더라도 이 안전한 완료를 기다린다. 성공하면 새 인증을 저장하지만 취소된 호출자에게 usable token을 반환하지 않는다. 실패·응답 유실·저장 실패는 기존 영속 차단 규칙을 따른다. 취소를 이유로 blocked marker를 지우거나 교환을 반복하지 않는다.
- 공통 모델 handler는 resolver 전후와 전송 직전에 취소를 확인한다. 전송 전에 관측한 취소는 `408 request_canceled`로 기록하고 모델 시도 횟수를 증가시키지 않는다. 루트·보조 handler의 실패 고정과 busy/activity 해제 경로를 그대로 따른다. CLI 연결이 이미 끊겼다면 이 HTTP 응답을 CLI가 수신하지 못할 수 있지만 로컬 진단은 남는다.
- 취소된 요청은 자동 재전송하지 않는다. 앱 진단은 CLI 입력 대기 상태를 확인한 뒤 필요한 경우 새 지시를 입력하도록 안내한다. 새 의존성은 없다.

## 한계

즉시 취소 대상은 mutation 잠금 대기다. 동기 Keychain 읽기·쓰기까지 중단하지 않으며 이미 시작한 OAuth와 저장은 안전하게 완료한다. 실제 전송과 동시에 발생하는 취소는 HTTP transport의 context 처리를 따른다. 전송 직전 확인만으로 서버가 요청을 받지 않았음을 보장하지 않는다.

## 검증

- `TestRequestAccessCanceledBeforeMutation`: 사전 취소와 mutation 잠금 대기 취소의 무교환·무저장.
- `TestRequestAccessCancellationPreservesRefreshOutcome`: 시작한 교환 중 호출자 취소, 동시 대기자 이탈, 성공 토큰·이력 소유권의 재시작 후 유지, 네트워크·저장 실패 후 영속 차단 및 단일 교환.
- `TestRequestAccessContextPropagation`: context 진입점 우선 선택과 deadline 전달.
- `TestCanceledRequestNeverDispatches`: resolver 전·후 및 전송 직전 취소의 모델 무전송.
- `TestAuxiliaryCanceledAuthenticationWait`: 포트 없이 보조 handler의 대기 취소, busy 해제, 실패 고정과 모델 무전송.
- `TestProbeCanceledAuthenticationWait`: 실제 managed 프록시의 메인·보조 요청 취소, 별도 갱신을 해제하기 전 busy 해제와 실패 재전송 차단, 갱신 결과 영속 저장.
- `TestProbeDiagnosticRedaction`, `DirectProxyCheck`: 취소 진단의 원문 비노출 및 앱 안내.
- 전체 검증 명령: `sh macos/Checks/verify.sh`. 실제 계정·실행 중인 사용자 daemon은 변경하지 않는다.

2026-09-21: 계정 관리자 전체 race 검사, 프록시·사용량의 갱신/취소 관련 race 검사, 포트 없는 보조 handler·진단·이력 사전 검사, `go vet ./...`, Swift 빌드와 `DirectProxyCheck`를 통과했다. 실제 HTTP 서버를 사용하는 `TestProbeCanceledAuthenticationWait`는 샌드박스의 포트 바인딩 제한으로 실행하지 못했다. 권한 확대 실행의 자동 승인 검토도 실패하여 이번 변경의 전체 통합 검증은 대기 상태다.
