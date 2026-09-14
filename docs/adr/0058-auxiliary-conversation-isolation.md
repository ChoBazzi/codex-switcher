# ADR 0058: 같은 CLI의 보조 대화 격리

- 상태: accepted
- 날짜: 2026-09-14
- 관련: ADR 0012, 0043, 0049, 0055, 0057

## 문제와 결정

CLI 0.154.0의 리뷰·서브에이전트·자동 승인 요청은 Thread-Id와 Session-Id가 서로 다른 정상 UUID다. 두 값의 동일성을 강제하면 `probe_identity_invalid`가 발생한다. 기존 대화 handler에 모두 합치는 방식은 도구 이력·실패 상태·계정 소유권을 섞으므로 채택하지 않는다.

`--tools` 앱 경로에서 인증된 보조 대화를 독립 처리한다. 새 `cliidentity.Conversation`은 각 헤더가 정확히 하나의 정상 UUID인지 검사한다. 기존 `ThreadID`의 엄격한 계약은 다른 경로를 위해 유지한다. X-Switcher-Run 인증을 먼저 검사하며, 보조 요청의 Session-Id는 연결의 루트 대화 ID와 같아야 한다. 리뷰가 최초 요청이면 그 Session-Id로 루트를 바인딩한다. 헤더 자체는 인증 수단이 아니며 로컬 연결 capability를 가진 단일 CLI를 신뢰하는 기존 경계다.

- 보조 Thread-Id마다 별도 handler, HTTP/도구 진행 상태, 실패 잠금, 계정 및 credential 해시를 둔다. 최대 128개이며 무단 eviction이나 재사용 없이 새 연결로 정리한다.
- 보조 작업은 최초 선택 계정에 고정한다. 같은 계정의 도구 후속만 허용하고 credential 해시가 달라지면 차단한다. 토큰 갱신도 해시 변경으로 이어질 수 있으므로 장기 보조 작업은 새 작업으로 시작해야 할 수 있다.
- 루트와 형제 보조 요청은 동시에 처리할 수 있다. 같은 보조 대화의 중첩 HTTP 요청은 거절하되 완료 이벤트 전달 직후의 후속 요청은 기존 처리가 끝나기를 제한 시간 동안 기다린다.
- 앱의 busy는 루트와 모든 보조 HTTP/도구 작업을 합산한다. 처리 중 수동 계정 전환·서비스 종료를 막고 자동 계정 선택도 보조 작업이 끝날 때까지 보류한다. 계정 추가/삭제와 배타적인 기존 activity lease는 전체 작업 묶음에서 공유한다. 각 자식이 별도 배타 잠금을 잡아 부모와 충돌하지 않는다.
- 모든 HTTP 요청이 끝난 뒤 도구 대기가 남았으면 기존 명시적 중단 확인으로 잠금을 해제할 수 있다. 보조 작업도 실패로 고정하며 도구를 재실행하지 않는다. 사용자는 전체 CLI 도구가 중단됐는지 확인해야 한다.
- 보조 실패는 해당 보조 대화에만 고정한다. 같은 요청이나 새 입력으로 자동 해제하지 않으며 실패 요청은 재전송하지 않는다. 새 명시적 리뷰/자식 작업은 새 Thread-Id로 시작한다. CLI가 부모 종료 코드 0을 반환해도 보조 성공을 뜻하지 않는다.
- 입력은 기존 텍스트/로컬 도구 이력 검증과 서버 참조 제거 정책을 따른다. 새 보조 handler는 부모의 opaque reasoning 소유권을 추정하지 않는다. 보조 native compaction/암호화 compaction 이력은 이번 단계에서 차단한다. 루트 텍스트·native 압축과 계정 소유권 정책은 유지한다.
- 보조 바인딩(Thread/Root/Slot)을 기존 비공개 checkpoint에 첫 upstream 전송 전에 저장한다. 본문·OAuth·credential 원문을 저장하지 않는다. 복구된 모든 보조 바인딩은 재전송 금지 상태로 시작한다. 기존 보조 작업의 자동 복원은 지원하지 않으며 새 작업을 사용한다. 예전 checkpoint는 빈 목록으로 읽는다. 이전 helper는 새 필드를 거절하므로 다운그레이드 시 새 연결이 필요하다.
- 서로 다른 루트의 `/new`, `/fork`, 다른 CLI/IDE 대화 공유는 계속 명시적 새 연결을 필요로 한다. 보조 대화 지원을 임의 다중 세션 라우팅으로 확대하지 않는다.

새 의존성 없음. 현재 작업은 같은 CLI의 보조 작업 지원이며 다중 사용자/원격 서비스 지원이 아니다.

## 검증 및 적용

`TestInstalledProbeAuxiliaryIdentity`: 설치 CLI → 실제 프록시 → 합성 upstream에서 review/spawn/spawn_fork/guardian 통과, 같은 root resume 유지, 다른 root fork/new 거절. 실제 계정과 동일한 activity 잠금도 사용한다.

`TestAuxiliaryFailureIsolationAndAccountPinning`, `TestAuxiliaryCheckpointFailurePreventsDispatch`, `TestAuxiliaryBindingSurvivesServiceRestart`: 실패 재전송 차단, 형제 격리, 계정 고정, checkpoint 오류 시 전송 없음, 인증/부모 관계 거절, 재시작 후 보조 재전송 거절과 새 보조 요청 허용을 검사한다. 전체 검사는 `sh macos/Checks/verify.sh`다.

새 helper를 빌드한 뒤 모든 CLI/보조 작업이 끝났을 때 앱 종료 → `switcher-helper proxy-stop` → 앱 재실행으로 적용한다. 실행 중인 기존 daemon은 빌드만으로 교체되지 않는다. 최초 적용 전 이미 차단된 리뷰/서브에이전트는 사용자가 새로 시작한다. 실제 계정 자동 검토의 승인 판단 품질과 모든 TUI 기능을 검증했다는 뜻은 아니다.

## 실사용 재발 진단

새 helper 적용 후에도 같은 상위 identity 오류가 보고되어, 인증된 요청의 오류 본문에 고정 진단 분류를 추가한다. thread/session 누락·중복·UUID 형식 오류·정상 불일치, 그리고 tools 비활성 경로를 구분한다. 실제 헤더 값·대화 ID·인증은 출력하지 않으며 수락 조건은 변경하지 않는다. 과거 이벤트는 보관되지 않아 소급 진단할 수 없다. CLI 0.154.0의 임시 홈 TUI `/review`에서도 각각 한 개의 서로 다른 정상 UUID를 확인했지만, 사용자의 실패 요청 자체를 재현한 것으로 간주하지 않는다.
