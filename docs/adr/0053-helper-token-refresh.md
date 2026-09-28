# ADR 0053: helper 소유 토큰 갱신과 모호한 실패 차단

- 상태: accepted (합성 인증 검사 통과; 실제 OAuth 응답 호환성 미검증)
- 날짜: 2026-09-11
- 관련: ADR 0001, 0013, 0049, 0054

2026-09-21: [ADR 0061](0061-authenticated-history-ownership.md)에서 이력 소유권을 계정·사용자 신원, 로그인 등록 세대와 실제 dispatch 인증 해시로 강화했다. 아래 슬롯 또는 토큰 전용 소유권 설명은 해당 결정을 따른다.

2026-09-21: [ADR 0065](0065-proxy-responsiveness-and-verification.md)에 따라 mutation 잠금과 로컬 조회 잠금을 분리한다. OAuth 대기 중에도 로컬 조회를 허용하며 mutation·프로세스 간 lock과 실패 marker는 저장 완료까지 유지한다.

## 결정

2026-09-21: [ADR 0067](0067-cancellable-authentication-wait.md)에 따라 인증 잠금 대기는 요청 context로 취소한다. 영속 갱신 의도 저장이 시작된 뒤에는 독립 timeout 아래 교환·저장을 마치며, 취소된 모델 요청을 전송하지 않는다.

앱 프록시의 사용량 조회와 실제 모델 dispatch에서 `RequestAccess`를 사용한다. 만료까지 2분 이하이면 helper가 OAuth refresh exchange를 한 번 수행한다. `Access`, `Status`, 계정 선택 및 등록 상태 확인은 로컬 읽기로 유지한다. 별도 `usage`/legacy `exec` 명령에는 갱신 권한을 추가하지 않는다.

갱신 endpoint와 public client ID는 설치된 Codex CLI 0.154.0 바이너리에서 기존 값의 존재를 확인했다. 요청은 `grant_type=refresh_token`, `client_id`, `refresh_token` JSON이며 응답의 access token은 필수, refresh/id token은 반환된 경우 교체한다. 이것은 공개 OAuth 호환성 보증이 아니다. 이번 환경에서 공식 문서 조회가 DNS 제한 및 자동 승인 검토 오류로 실패했으므로 실계정 응답 호환성은 별도 검증한다.

- 계정 manager의 mutex와 로그인/로그아웃에 사용하는 프로세스 간 `account-operation.lock`을 공유한다. 잠금을 얻은 뒤 Keychain을 다시 읽어 다른 프로세스가 갱신·제거한 값을 덮어쓰지 않는다. 잠금 경합 시 요청을 재전송하거나 잠금을 우회하지 않는다.
- 네트워크 전송 전에 해당 Keychain 레코드에 `refresh_blocked=true`를 저장한다. 성공 응답의 계정 ID와 사용자 ID가 기존 계정과 같고 만료 시각이 충분히 미래인지 검증한 후 새 토큰과 marker 해제를 함께 저장한다.
- 실패·응답 유실·프로세스 중단·성공 후 저장 실패는 자동 재시도하지 않는다. marker가 남으면 다음 polling/dispatch/helper 재시작에서도 재교환하지 않고 인증 만료 상태로 안내한다. 명시적 동일 계정 재로그인 성공으로만 해제한다. 다른 슬롯에는 영향을 주지 않는다.
- HTTP 전송은 15초 제한, redirect 금지, 프록시 환경 변수 미사용, keep-alive/HTTP2 비활성화, 재생 불가능한 request body를 사용한다. 반환 본문은 64 KiB로 제한하고 오류 원문을 노출하지 않는다.
- 토큰과 실제 식별자는 Keychain 밖의 영속 저장소나 로그에 기록하지 않는다. 레코드 version 1과 기존 슬롯은 유지하며 boolean marker만 추가한다. 외부 의존성 없음.
- 모델 요청 실패를 갱신의 트리거로 삼지 않는다. 특히 401을 받은 모델 요청을 갱신 뒤 다시 보내지 않는다.

## 한계

일시적인 네트워크 실패도 자동 갱신 반복 대신 재로그인을 요구한다. 서버가 refresh token을 이미 소비했는지 알 수 없기 때문이다. CLI 텍스트 압축 경로는 그대로 유지한다. [ADR 0064](0064-verified-refresh-history-continuity.md)에 따라 동일 신원을 검증한 갱신은 새 토큰과 이력 소유권 연결을 Keychain에 함께 저장한다. 검증되지 않은 변경으로 기존 opaque 항목의 소유권을 이전하지 않는다. 실제 서비스의 갱신 후 암호화 이력 호환성은 별도 검증한다.

## 검증

`go test -race ./internal/accounts`는 로컬 상태 조회 무호출, 16개 동시 요청의 단일 갱신, 재시작 후 새 토큰 재사용, 네트워크/계정 불일치/만료/저장 실패의 영속 차단, 재로그인 해제, 계정 변경 잠금, HTTP redirect·오류·불완전·과대 응답과 선택적 토큰 필드를 합성 자료로 검증한다. OAuth HTTP 검사는 메모리 transport를 사용하며 실제 endpoint/Keychain을 호출하지 않는다.
