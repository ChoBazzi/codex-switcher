# ADR 0054: 앱과 수명이 분리된 단일 프록시 서비스

- 상태: accepted (빌드·메모리 제어 검사 통과; 로컬 서버 통합 실행 검증 대기)
- 날짜: 2026-09-11
- 관련: ADR 0001, 0034, 0041, 0048, 0053

2026-09-11: 독립 daemon의 crash 복구·연결 정보와 compaction 소유권 해시 보존은 [ADR 0055](0055-proxy-crash-session-recovery.md)가 아래 재시작 시 유실 설명을 대체합니다.

2026-09-14: 정상 앱 종료는 프록시 종료까지 확인하도록 [ADR 0059](0059-app-and-proxy-lifecycle.md)에서 변경했다. 아래 앱 종료 후 daemon 유지 설명은 과거 동작이며, 독립 프로세스와 crash 복구 구조는 유지한다.

2026-09-21: [ADR 0062](0062-safe-diagnostics-and-build-status.md)에서 원문을 제외한 고정 오류 분류의 메모리 캐시와 앱 전달, 실행 프록시/relay 빌드 비교를 추가했다.

## 결정과 선택지

메뉴바 앱이 소유하던 `switch-probe --managed`를 독립 프로세스 `proxy-daemon` 안에서 실행한다. 앱은 `proxy-connect` relay만 소유한다. 기존 stdin JSON 명령·상태 형식과 프록시의 전환/실패/도구/압축 정책을 재사용한다.

첫 연결에서 서비스가 없으면 detached session으로 한 번 시작하고, 이후 앱 재실행은 같은 서비스에 연결한다. relay의 표준 입출력은 앱에 연결되지만 daemon의 표준 스트림은 `/dev/null`이며 앱 파이프를 상속하지 않는다. 앱 종료는 relay만 종료한다. 모델 HTTP 연결, CLI profile, 현재 계정, 대화·도구 턴과 실패 차단, 사용량 polling은 daemon에 남는다. 앱 종료 버튼은 모델 요청 중에도 허용하며 로그인 작업 중 차단은 유지한다.

LaunchAgent 설치는 로그인 시 자동 시작이 필요하지 않은 현재 범위보다 넓으므로 보류한다. daemon 자체의 crash/reboot 복구는 이번 보장의 대상이 아니다. 재시작된 daemon은 새 profile을 만들며 이전 실패 요청이나 대화를 자동 재생하지 않는다.

## 제어 경계

ADR 0001의 HTTP bearer 제어 대신 앱 서비스 연결에는 사용자 전용 Unix domain socket을 사용한다. 경로는 Application Support의 `com.bazzi.codex-switcher/runtime/control.sock`, 디렉터리는 소유자 확인 및 0700, 소켓은 0600이다. 외부 TCP 제어 포트와 별도 secret 파일이 필요 없다. 모델 프록시의 loopback HTTP와 실행별 인증 secret은 유지한다. 기존 legacy 상태 서버는 앱 소유로 유지하며 모델 서비스 수명과 관계없다.

서비스 lifetime flock과 launcher flock을 분리한다. stale 소켓은 lifetime lock을 확보한 서비스만 제거하며 일반 파일·symlink는 덮어쓰지 않는다. 제어 연결은 한 번에 하나만 허용한다. 같은 OS 사용자 프로세스는 제어할 수 있으며 다중 사용자 보안 경계를 제공한다고 주장하지 않는다.

- ready/profile, 최신 사용량 및 최신 상태만 메모리에 보관한다. 재연결 때 상태를 재전달하며 과거 선택 ACK를 다시 보내지 않는다.
- 수동 조회 request ID를 서비스 번호로 치환하고 완료 이벤트를 원래 relay에만 보낸다. 이전 앱의 완료 응답이 새 앱의 같은 번호 요청을 완료시키지 않는다.
- UI 송신은 제한된 큐와 쓰기 timeout을 사용한다. UI가 읽지 않으면 해당 연결만 닫고 모델 스트림을 막지 않는다. 모델 진단 이벤트/내용은 재연결 cache에 넣지 않는다.
- 모델 요청부터 로컬 도구 턴 종료까지 별도 activity flock을 유지한다. 로그인/재로그인/로그아웃도 같은 flock을 획득해야 하므로 앱이 없거나 상태가 오래돼도 진행 중 인증을 교체할 수 없다. 명시적 도구 중단 확인과 실패/정상 완료에서 해제한다. 토큰 갱신은 별도 계정 잠금으로 직렬화한다.
- 앱이 계정 변경 완료 알림 전에 종료되면 후속 상태 조회에서 계정 작업 잠금이 해제된 것을 확인하고 미완료 슬롯 상태를 로컬 재확인한다. 오래된 사용량을 되살리지 않는다.

## 명시적 서비스 종료

`switcher-helper proxy-stop`은 기존 서비스에만 연결해 종료를 한 번 요청한다. 요청·도구 턴·후속 요청 대기 중이면 거절한다. 서비스가 idle일 때 종료 ACK를 전달한 뒤 종료한다. 앱을 먼저 종료해 제어 연결을 해제하고 사용한다. 앱이 열려 있으면 단일 제어 연결 정책으로 거절될 수 있다. 종료/재연결 버튼만으로 모델 요청을 시작하지 않는다.

새 CLI 대화를 시작하거나 helper 업데이트를 적용하려면 기존 CLI 턴을 끝내고 앱 종료 → `proxy-stop` → 앱 실행 → 새 연결 명령 순서로 진행한다. 앱 재실행만으로 실행 중인 구 helper 바이너리를 교체하지 않는다. 외부 의존성 없음.

## 검증

- `TestServiceDetachPreservesStateAndAckIsolation`: 메모리 연결 단절 후 상태·profile 보존 및 이전 ACK 격리.
- `TestServiceLifecycleAndSingleInstance`: 실제 로컬 소켓의 권한, 단일 인스턴스, 앱 단절 후 서비스 생존.
- `TestServiceStreamSurvivesReconnect`: 합성 HTTP 스트리밍 중 종료 거절·앱 단절·완료·재연결, 동일 profile/대화 상태와 upstream 1회, activity lock 유지/해제 및 idle 종료.
- `TestServiceRefusesForeignPath`: 기존 일반 파일 보존.
- Swift `DirectProxyCheck`: relay 명령 경로, 선택/조회 ACK·취소·연결 해제 처리.

현재 sandbox에서 로컬 서버 bind가 차단되고 테스트 승격도 자동 승인 검토 오류로 거절됐다. 따라서 실제 소켓과 HTTP를 사용하는 두 테스트 및 전체 `sh macos/Checks/verify.sh`는 실행 검증이 남아 있다. 메모리 기반 검사·Go 전체 컴파일/정적 검사·Swift 앱 빌드는 통과했다. 실제 계정과 기존 daemon을 개발 검증에서 실행하거나 종료하지 않았다.
