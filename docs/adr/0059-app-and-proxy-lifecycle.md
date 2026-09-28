# ADR 0059: 앱과 프록시 함께 시작·종료

- 상태: accepted
- 날짜: 2026-09-14
- 관련: ADR 0054, 0055, 0058

제어 relay 종료 직후 쓰기 경쟁으로 앱이 종료되는 문제는 [ADR 0060](0060-relay-broken-pipe-recovery.md)에서 보완한다. 실패한 종료 명령을 성공으로 추정하거나 재전송하지 않는 계약은 유지한다.

## 결정

사용자는 앱만 관리한다. 앱 시작 시 기존 proxy-connect 경로로 서비스를 시작하거나 연결한다. 정상 앱 종료 시 현재 relay에 shutdown(new_session=false)을 한 번 보내고, 성공 ACK와 연결 종료를 확인한 뒤 앱을 종료한다. 독립 daemon 구조와 checkpoint 복구 계약은 유지한다. ADR 0054의 정상 앱 종료 시 daemon을 남기는 기본 동작을 대체한다.

NSApplicationDelegate의 terminateLater를 사용해 메뉴 버튼·Cmd-Q·시스템 종료 요청을 동일하게 처리한다. 로그인/로그아웃 작업 중에는 종료를 거절한다. 프록시는 루트와 보조 HTTP·도구·후속 대기 상태를 검사해 종료를 거절하며 앱에 안내창을 표시한다. idle 종료 수락 시 같은 mutex 안에서 closing을 먼저 설정해 ACK 이후 새 모델 요청이 진입하지 못하게 한다.

앱은 성공 ACK만으로 즉시 종료하지 않고 relay EOF도 기다린다. 거절·미연결·전송 오류·8초 timeout·ACK 없는 단절은 정상 종료로 추정하지 않는다. 자동 재전송, 강제 daemon kill 또는 새 세션 초기화는 하지 않는다. 상태 확인 후 다시 종료할 수 있다. 종료 중에는 앱 polling과 중복 종료 요청을 억제한다.

앱 재실행 시 동일 주소·CLI 홈·대화 상태를 복구한다. 사용자는 기존 CLI에서 새 지시를 입력한다. 앱이 꺼져 있는 동안에는 프록시가 없어 요청할 수 없다. 실패한 요청 및 보조 작업은 자동 재전송하지 않는다. 앱 강제 종료·crash는 graceful 종료를 보장하지 않으며 남아 있는 daemon에 다음 앱 시작 시 연결한다. 새 의존성 없음.

## 검증

DirectProxyCheck의 합성 relay에서 도구 대기 중 종료 거절, idle ACK 및 EOF 이후 성공, new_session=false 유지, 미연결 종료 거절을 검사한다. 기존 ServiceStream 검사에서 활성 스트림 종료 거절과 idle 종료를 확인한다. 전체 `sh macos/Checks/verify.sh`로 Go race/vet·설치 CLI·Swift/UI 회귀를 확인한다.

수동 확인: 작업 완료 → 앱·프록시 종료 → 메뉴바 아이콘과 모델 listener 종료 → 앱 재실행 → 같은 CLI에서 새 입력. 작업 중 종료 또는 Cmd-Q는 안내 후 앱과 프록시가 유지되어야 한다. 앱 강제 종료는 이 검증에 포함하지 않는다. 최초 적용은 새 앱 실행 파일로 다시 실행해야 하며 개발 검사는 사용자 daemon을 종료하지 않는다.
