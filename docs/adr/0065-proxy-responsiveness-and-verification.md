# ADR 0065: 프록시 대기 제한, 진단 및 검증 자동화

- 상태: accepted
- 날짜: 2026-09-21
- 관련: ADR 0053, 0058, 0059, 0060, 0062, 0064

## 배경과 선택지

본문 수신에 크기 제한만 있어 멈춘 CLI가 계정 activity lease를 계속 보유할 수 있었다. OAuth 갱신은 로컬 계정 조회 및 루트 프록시의 상태 mutex를 함께 막았다. 본문 사전 검사의 인증 오류는 소유권 없음으로 바뀌어 재로그인이 필요한 상황에서 새 대화를 안내했다. 보조 대화 128개 한도도 일반 거절로 표시됐다.

전체 요청에 짧은 timeout을 적용하면 긴 모델 응답까지 중단되므로 입력 수신만 제한한다. OAuth나 실패 요청을 다시 보내는 방식은 사용하지 않는다. 계정 mutex를 단순히 제거하면 인증 변경 경쟁이 생기므로 네트워크 대기와 상태 조회의 잠금을 분리한다.

## 결정

2026-09-21: [ADR 0067](0067-cancellable-authentication-wait.md)에서 mutation 잠금 대기를 취소 가능하게 변경한다. 시작한 갱신의 안전한 저장과 기존 실패 차단은 유지한다.

- managed probe HTTP 서버에 30초 `ReadTimeout`, 기존 5초 `ReadHeaderTimeout`을 적용한다. 요청 본문은 기존 4 MiB 제한을 유지한다. 응답 쓰기 제한은 추가하지 않고 기존 upstream 10분 제한을 유지한다. 루트·보조 본문 실패는 timeout 408, 크기 초과 413, 기타 수신 실패 400으로 분류한다. 기존 실패 고정 및 activity 해제 경로를 따른다.
- 계정 관리자에 mutation 전용 `operationMu`를 둔다. 로그인·로그아웃·RequestAccess는 이 잠금으로 직렬화한다. 갱신은 프로세스 간 계정 작업 lock과 mutation 잠금을 저장 완료까지 유지하되, OAuth 네트워크 대기 중 로컬 조회 mutex만 해제한다. Keychain commit 전후 경계와 단일 exchange 원칙은 유지한다.
- 같은 manager의 갱신 중 이력 조회에는 직전 소유권만 제공한다. 실제 dispatch는 mutation 잠금을 기다린 뒤 저장된 결과를 확인한다. usable `Access`는 blocked marker를 계속 거절한다. 실패/재시작 후에는 진행 중인 exchange가 없으므로 이력 조회도 거절한다. 메모리의 진행 상태로 marker를 지우거나 저장 실패를 성공으로 취급하지 않는다.
- 루트 resolver는 선택 슬롯·대화를 상태 잠금 아래 캡처하고 잠금 밖에서 `RequestAccess`를 수행한다. 이후 다시 잠그고 종료·checkpoint 오류·슬롯/대화 변경·요청 취소와 이력 소유권을 확인한다. 모델/도구 busy 및 activity lease는 이 과정 내내 유지한다. UI 상태 조회와 종료 거절 응답은 OAuth 완료를 기다리지 않는다.
- 이력 조회에서 인증 오류를 버리지 않는다. 인증 만료는 401, 계정 부재는 401, Keychain 읽기/쓰기 실패는 503으로 구분한다. 임의 오류 원문은 기존 일반 코드로 정제한다. Keychain 오류 sentinel은 루트·보조 dispatch에도 공통 적용한다.
- 보조 대화는 128개 제한과 기존 tombstone을 유지한다. 상태 이벤트에 개수·한도를 보내고 앱의 상태·진단 및 복사 내용에 남은 용량을 표시한다. 한도 도달은 별도 고정 코드로 안내한다. 구버전의 필드 누락은 미확인으로 표시한다. 기록을 자동 삭제하거나 실패한 Thread-Id를 재활용하지 않는다.
- 초기화는 기존 명시적 `proxy-stop --new-session`을 이용한다. README에 전체 작업/CLI 종료 → 연결 종료 → 앱 재연결 → 새 CLI 시작 절차와 이전 홈·기록 보존을 설명한다. 앱 종료·일반 재시작에는 새 세션 초기화를 추가하지 않는다.
- Swift relay는 `Darwin.read`로 최대 4 KiB씩 읽고 줄 단위로 나눈다. UTF-8 경계, 여러 이벤트를 포함한 한 번의 읽기, 8 KiB 줄 제한, 중단된 마지막 줄 미전달, relay 세대 검사와 SIGPIPE 보호를 유지한다. 요청을 추가하거나 재전송하지 않는다.
- macOS GitHub Actions에서 전체 race/vet, helper·Swift 빌드, 설치 CLI 합성 검사와 UI 검사를 실행한다. CLI는 기존 검증 버전 0.155.1에 고정한다. 애플리케이션 런타임 의존성은 추가하지 않으며 CI의 Node/npm은 테스트용 CLI 설치에만 사용한다. 브랜치 보호의 필수 검사 설정은 외부 저장소 설정이므로 이 변경에서 활성화하지 않는다.

## 검증

- `TestRefreshKeepsLocalReadsAvailable`: 갱신 성공/실패 대기 중 상태·소유권 조회, usable auth 차단, 동시 dispatch 합류와 최종 영속 결과.
- `TestProbeStatusAvailableDuringRefresh`: 실제 managed 프록시에서 OAuth를 멈춘 동안 상태 명령과 작업 중 종료 거절 응답, 해제 후 정상 단일 후속 요청.
- `TestProbeBodyDeadline`: 포트 없는 `net.Pipe`와 실제 HTTP 서버에서 중단 업로드 timeout, 본문 수신 후 긴 응답 유지.
- `TestAuxiliaryBodyFailureReleasesAndNeverReplays`: 수신 실패의 busy 해제·실패 고정·upstream 무호출·재전송 차단.
- `TestProbeAuthenticationPrevalidationDiagnostic`, `TestResolverDiagnosticAllowlist`, `TestProbeDiagnosticRedaction`: 갱신 실패가 이력 오류로 바뀌지 않는지, 저장소 오류 및 새 진단의 원문 비노출.
- `TestProbeAuxiliaryCapacityAdmission`, `DirectProxyCheck`: 용량 차단 사유·앱 남은 용량, 한글 바이트 분할·여러 이벤트·줄 크기 경계 및 기존 relay 재연결 회귀.
- 전체 검증: `sh macos/Checks/verify.sh`. 실제 계정·사용자 daemon은 변경하지 않는다. CI 워크플로 자체의 hosted runner 실행과 실계정 OAuth 호환성은 로컬 합성 검증과 별개다.

2026-09-21: 전체 검증 통과. Go race/vet, 설치 CLI 합성 통합 검사, helper·Swift 빌드와 밝은/어두운 UI 검사를 완료했다. 기존 Swift `onChange` deprecation 경고는 남아 있으며 검증 실패는 아니다. GitHub Actions 실행 및 브랜치 보호 설정은 로컬에서 실행하지 않았다.
