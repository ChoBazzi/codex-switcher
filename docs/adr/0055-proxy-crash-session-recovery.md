# ADR 0055: 프록시 재시작 후 같은 CLI 대화 복구

- 상태: accepted (전체 Go·설치 CLI·Swift/UI 합성 검증 통과)
- 날짜: 2026-09-11
- 관련: ADR 0054, 0049, 0048, 0045, 0043

## 배경과 선택지

독립 daemon은 앱 종료에는 살아 있지만, daemon 자체가 종료되면 임시 주소·profile과 메모리의 대화/소유권 상태가 사라졌다. 기존 CLI는 실행 시 읽은 주소를 계속 사용하므로 새 profile을 만드는 것으로는 연결을 복구할 수 없다.

1. CLI를 새 대화로 시작하고 Wiki로 인계: 기존 화면과 대화 기록을 그대로 이어가려는 요구를 충족하지 못한다.
2. daemon의 연결 정보와 최소 상태를 비공개 파일에 원자 저장: 선택. 단일 CLI 상태이므로 별도 DB/의존성을 추가하지 않는다.
3. LaunchAgent/watchdog로 자동 재시작: 이번 범위에서는 보류한다. 앱의 `모델 프록시 다시 연결` 또는 앱 재실행으로 서비스를 시작한다. 모델 요청을 재시도해서 서비스를 깨우지 않는다.

## 결정

독립 daemon만 Application Support의 `com.bazzi.codex-switcher/runtime/checkpoint.json`을 사용한다. 기존 실험용 switch-probe는 임시 동작을 유지한다. 서비스 lifetime lock 안에서 읽고, 모든 HTTP 처리 및 상태 저장을 마친 뒤 lock을 해제한다.

- 같은 loopback 포트, CLI 홈과 로컬 연결 secret을 재사용한다. CLI 홈은 runtime 아래 0700 디렉터리에 두고 대화 기록과 config를 삭제하지 않는다. 복구 시 사용자가 수정한 config를 덮어쓰지 않는다.
- 대화 식별자, 선택 슬롯, 이전 슬롯, 요청/도구 대기/실패 상태, 명시적 복구의 새 입력 조건, 요청·마지막 사용자 경계의 해시와 revision을 저장한다. 사용량은 복원하지 않고 새로 조회한다.
- 파일은 0600이며 임시 파일 쓰기·fsync·rename·디렉터리 fsync로 교체한다. 읽을 때 소유자·권한·일반 파일·단일 링크·버전·범위를 검사하고 symlink를 거절한다. 손상/권한 오류나 기존 포트 점유 시 새 상태로 우회하지 않고 시작을 거절한다.
- 마지막 저장 성공 상태와 복구 정보 전체가 같으면 파일 쓰기와 fsync를 생략한다. 압축 소유권 목록의 순서는 변화로 취급하지 않는다. 초기 저장과 상태 변화는 기존처럼 즉시 동기 저장하고, 저장 실패는 캐시에 반영하지 않는다. 매초 상태 응답과 사용량 조회 주기는 유지한다.
- upstream 전송 전에 요청 경계와 inflight를 저장한다. 저장 실패 시 전송을 차단하고 서비스를 종료한다. 정상 종료 상태도 저장한다. 완료 전달 직전/직후 crash처럼 전달 여부가 불확실한 경우 성공으로 추정하지 않는다.
- 재시작 자체는 모델 요청을 시작하지 않는다. 정상 완료 상태에서도 기존 사용자 경계의 재전송은 차단하고 CLI의 새 사용자 입력을 기다린다.
- 요청/도구 처리 중 종료됐다면 실패 상태로 복구한다. 앱에서 같은 계정의 `복구 준비 · 새 입력 대기` 또는 다른 사용 가능한 계정을 선택한 뒤 CLI에 새 지시를 입력한다. 선택만으로 요청이나 도구를 재실행하지 않는다. 기존의 불완전 도구 이력 검증은 유지하므로 미완료 호출 쌍이 남으면 새 대화가 필요할 수 있다.
- native compaction은 원문 대신 항목 해시·슬롯·인증 토큰 해시로 소유권을 보존한다. 복구 후에도 같은 인증만 허용하고 다른 계정 전환을 차단한다. OAuth 토큰 원문, refresh token, Authorization 헤더, 실제 계정 ID, 요청/응답 본문은 checkpoint에 넣지 않는다. 로컬 연결 secret은 OAuth가 아니며 기존 config에도 있는 CLI→프록시 인증값이다.

`proxy-stop`은 상태를 보존한 채 idle 서비스를 종료한다. helper 업데이트 후 앱을 실행해도 기존 CLI 연결을 유지한다. **새 대화를 의도적으로 시작하려면** 앱 종료 후 `switcher-helper proxy-stop --new-session`을 사용한다. 활성 요청/도구 턴 중에는 거절하며, idle일 때 기존 checkpoint를 retired로 저장하고 종료한다. 다음 서비스는 새 홈과 연결 정보를 만든다. 과거 CLI 기록은 삭제하지 않는다.

## 결과와 한계

- daemon 강제 종료 후 같은 CLI 주소·대화·계정 선택으로 새 입력을 처리할 수 있다. 재부팅 후에도 디스크 정보는 보존되지만, 종료된 터미널 프로세스를 되살리지는 않는다. 앱 재실행 후 보존된 CODEX_HOME에서 Codex의 명시적 resume을 사용한다.
- 이미 구형 daemon에서 유실된 메모리 상태는 소급 복구할 수 없다. 이 버전의 helper와 앱을 적용하고 새 연결을 시작한 이후부터 보장한다.
- 포트 충돌, 저장 파일 손상, 인증 변경으로 소유권 확인 불가, 불완전 도구 이력은 무조건 자동 복구하지 않는다. 외부 도구가 crash 동안 계속 실행됐을 수 있으므로 복구 준비 전에 CLI 입력창 복귀를 확인한다.
- profile과 연결 secret의 수명이 길어지고 요청 경계마다 동기 디스크 쓰기가 발생한다. 기존 단일 사용자/단일 CLI 범위를 유지한다. 새로운 외부 의존성은 없다.

## 검증

- `TestProbeCheckpointKillAndResume`: 별도 합성 helper를 SIGKILL로 종료, 정상 완료/전송 중 재시작, 같은 주소·CLI 홈·사용자 config·계정·대화 연결 보존, 기존 입력 무호출 차단, 명시적 복구 후 새 입력 허용.
- `TestInstalledProbeCheckpoint`: 설치 CLI에서 합성 대화 생성, helper SIGKILL/재시작, 같은 thread ID로 resume하여 이전 사용자/assistant 텍스트가 다음 요청에 보존되는지 확인.
- `TestProbeCheckpointNativeCompaction`: native 압축 후 강제 종료/재시작, 동일 계정의 opaque 소유권 복원·타 계정 선택 거절, 명시적 새 세션과 기존 CLI 홈 보존.
- `TestProbeCheckpointWriteFailureBlocksDispatch`: checkpoint 저장 실패 시 upstream 호출 0회와 손상 상태 우회 거절.
- `TestProbeCheckpointOwnerRoundTrip`, `TestProbeCheckpointRejectsUnsafeStorage`: 소유권 해시·retirement·권한 및 손상/symlink 거절.
- `DirectProxyCheck`: 복구 가능한 실패 상태에서만 현재 계정 선택을 허용하고 busy일 때 차단.
- `TestProbeCheckpointStatusDoesNotWrite`, `TestProbeCheckpointComparison`: 반복 상태 조회 시 파일 inode/수정 시각 보존, 계정 변경 ACK 전 저장, 소유권 순서 무시 및 작업/인증 변화 감지.

`sh macos/Checks/verify.sh`로 전체 Go race/vet, helper 빌드, 설치 CLI 합성 검사, Swift 앱 빌드와 UI 검사를 통과했다. 실제 계정 모델 호출이나 사용자 daemon 종료는 검증에 사용하지 않는다. 실제 메뉴바 앱에서의 강제 종료/복구와 macOS 재부팅 수동 검증은 별도다.
