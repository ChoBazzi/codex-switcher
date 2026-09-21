# ADR 0064: 검증된 토큰 갱신의 이력 소유권 유지

- 상태: accepted (전체 합성 통합 검증 통과; 실계정 검증 대기)
- 날짜: 2026-09-21
- 관련: ADR 0049, 0053, 0055, 0058, 0061

2026-09-21: [ADR 0065](0065-proxy-responsiveness-and-verification.md)는 갱신 중 로컬 읽기 잠금과 dispatch 잠금을 분리한다. 같은 manager에서 진행 중인 exchange에 한해 로컬 이력 조회는 직전 소유권을 반환하고, 실제 dispatch는 갱신 결과 저장을 기다린다. 실패·재시작 후 차단 규칙은 유지한다.

## 배경과 선택지

ADR 0061의 소유권은 계정·사용자·등록 세대와 access token을 함께 해시한다. 동일 신원의 정상 갱신도 해시를 바꾸므로 도구 후속의 reasoning, native 압축 이력, 장기 보조 작업이 거절될 수 있다. 또한 루트 본문 검사가 만료된 `Access`를 거절하면 실제 dispatch의 갱신 기회에 도달하지 못한다.

토큰을 해시에서 무조건 제외하면 helper가 검증하지 않은 인증 교체까지 동일 소유권을 얻는다. 과거 토큰 해시 목록을 계속 늘리는 대신, 기존 소유권과 현재 인증을 연결하는 고정 크기 자료를 신뢰 경계인 Keychain에 둔다. 클라이언트 헤더·본문이나 계정 변경 알림은 갱신의 증거로 사용하지 않는다.

## 결정

- 각 Keychain 레코드에 선택적 `history`를 둔다. `owner`는 갱신 전 유효한 이력 소유권 해시, `current`는 갱신 후 계정·사용자·등록 세대·토큰의 ADR 0061 형식 해시다. 이전 토큰 원문이나 토큰별 해시 목록은 추가로 보관하지 않는다.
- helper의 단일 OAuth exchange가 성공하고 계정·사용자 신원과 만료 시각을 확인한 경우에만 기존 owner를 유지한다. 새 credentials, history 연결, `refresh_blocked` 해제를 Keychain에 함께 저장한 뒤 사용한다. 반복 갱신도 최초 유효 owner를 유지하며 갱신 횟수만큼 저장량이 늘지 않는다.
- `Access.HistoryCredential`은 현재 인증에서 계산한 해시가 저장된 current와 일치할 때만 owner를 반환한다. 일치하지 않는 오래된 연결은 무시하고 현재 인증 자체의 해시를 사용한다. 이후 정상 갱신도 이 새로운 소유권에서 시작하므로 오래된 소유권이 되살아나지 않는다. 내부 연결은 외부에서 설정할 수 없는 `Access` 필드에 담는다.
- 로그인·명시적 재인증은 새 등록 세대를 부여하고 연결을 제거한다. 로그아웃은 레코드를 제거한다. 등록 세대·계정·사용자가 바뀌거나 신원이 누락되면 이전 이력을 승인하지 않는다.
- 로컬 `Manager.HistoryCredential(slot)`은 만료 여부와 별개로 소유권 해시만 반환한다. usable token을 반환하거나 OAuth·모델 요청을 시작하지 않는다. 갱신 차단 marker가 있으면 소유권도 반환하지 않는다. 기존 `Access`의 만료 검사와 계정 선택 정책은 유지한다.
- 루트 파서는 이 로컬 조회를 사용하고 실제 dispatch에서는 기존 `RequestAccess` 뒤 소유권을 다시 검사한다. 따라서 본문 검사와 dispatch 사이의 사용량 갱신도 허용하지만 검증되지 않은 변경은 거절한다. 보조 작업과 native 압축은 공통 소유권 계산의 변경을 적용받는다.
- reasoning은 여전히 해당 대화의 성공한 응답에서 관측한 항목만 허용한다. 갱신은 임의 opaque 항목이나 다른 계정·대화의 이력을 승인하지 않는다. 과거 사용자 턴 reasoning 제거, 계정 전환 잠금, 보조 실패 고정, 자동 재전송 금지도 유지한다.
- 갱신 실패·신원 불일치·응답 유실·저장 실패는 ADR 0053의 영속 차단 정책을 따른다. 401 모델 응답을 갱신 뒤 재실행하지 않는다.

## 호환성과 한계

기존 Keychain 레코드를 읽는 것만으로 수정하지 않는다. 연결이 없으면 ADR 0061 해시를 그대로 사용하고 첫 정상 갱신이 그 해시를 이어받는다. 등록 세대가 없는 레코드도 기존 방식으로 구분한다. 기존 checkpoint 형식을 바꾸지 않으므로 native 압축 owner는 갱신 및 manager·프록시 재시작을 넘어 일치할 수 있다. 구형 토큰 전용 owner를 추정 변환하지 않는다.

일반 reasoning 관측 기록은 재시작 후 복원하지 않고, 보조 작업도 기존처럼 실패 tombstone으로 복구한다. 정상 갱신이 이전 실패 요청을 해제하거나 재시작 후 중단 작업을 재실행하지 않는다. 구형 helper가 새 연결을 제거하거나 새 형식을 해석하지 못하는 경우에는 연속성이 보장되지 않으며 소유권을 추정하지 않는다.

소유권 연결은 Keychain 내부에만 저장하고 checkpoint에는 기존 owner 해시만 남긴다. 토큰·실제 신원·등록 세대를 로그나 상태 API에 추가하지 않는다. 추가 의존성 없음.

합성 검사는 프록시의 소유권 정책을 확인한다. 실제 OAuth 응답과 서비스가 갱신된 토큰으로 과거 암호화 이력을 수락하는지는 별도 검증해야 한다. 서버가 거절해도 자동 재전송하거나 이력을 다른 계정으로 보내지 않는다.

## 검증

- `TestHistoryCredentialVerifiedRefresh`: 반복 갱신, manager 재시작, 인증 구성 요소 변경 및 재인증 후 격리.
- `TestHistoryCredentialExpiredLocalRead`, `TestHistoryCredentialStaleVaultBinding`: 만료 인증의 무호출·무수정 소유권 조회, 임의 토큰 교체 후 오래된 연결 재사용 금지.
- `TestRefreshFailureNeverReplaysAcrossRestart`: 네트워크·계정/사용자 불일치·만료·저장 실패의 영속 차단과 소유권 조회 거절.
- `TestProbeRefreshPrevalidation`: 만료 전송 인증과 로컬 소유권 조회의 분리, 실제 계정 관리자의 검증된 갱신 전후 이력 파서 통과, 관측하지 않은 이력 거절. 포트 사용 없음.
- `TestProbeVerifiedRefreshContinuity`: 실제 managed 프록시의 루트 reasoning·native 압축·보조 대화, dispatch/사용량 갱신, 만료 직전/후 후속 요청과 새 토큰 전달.
- `TestProbeRefreshFailureNeverDispatchesOrReplays`, `TestProbeRefreshedCompactCheckpoint`: 갱신 실패 후 모델 전송·재교환 차단, checkpoint 복원 전후 갱신의 압축 owner 유지.

`sh macos/Checks/verify.sh` 전체 검증을 통과했다. 이전에 권한 제한으로 실행하지 못했던 loopback 통합 검사도 실행하여, 테스트 계정 fixture 및 제어 응답 검사 조건을 보완한 뒤 갱신·이력 격리·checkpoint·실패 재전송 차단을 확인했다. Go race/vet, 설치 CLI 합성 통합 검사, helper·Swift 빌드 및 UI 검사를 포함한다. 실행 중인 사용자 앱·daemon 및 실제 계정은 변경하지 않았다.
