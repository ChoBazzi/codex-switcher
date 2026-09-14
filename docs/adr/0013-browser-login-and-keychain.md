# 0013. 격리 브라우저 로그인과 네이티브 Keychain 계정 저장

- 상태: accepted (계정 등록 단계)
- 날짜: 2026-09-06
- 관련: ADR 0001, 0003, 0007, 0009

## 결정 배경

기존 Codex 로그인 정보를 읽거나 변경하지 않고 계정을 추가해야 한다. OAuth URL, client ID 및 콜백 프로토콜은 재구현하지 않고 공식 `codex login`에 위임한다.

후속 [ADR 0053](0053-helper-token-refresh.md)에서 앱 프록시의 토큰 갱신을 추가했다. 아래 자동 갱신 미구현 설명은 계정 등록 단계의 기록이다.

## 구현 결정

- `switcher-helper account login a|b`, `account status`, `account reauth a|b`를 제공한다. SwiftUI 및 HTTP 로그인 제어 API를 만들기 전에 동일 인증 코어를 터미널에서 검증한다.
- Application Support의 `com.bazzi.codex-switcher` 전용 디렉터리를 0700으로 생성한다. 소유자 및 권한을 확인한 0600 lock 파일에 프로세스 간 배타 잠금을 걸어 계정 변경을 직렬화한다. lock 파일은 unlink하지 않는다.
- 로그인마다 새 0700 디렉터리를 생성하고 `CODEX_HOME`으로 지정한다. child 작업 디렉터리도 이 디렉터리다. 공식 CLI는 file credential store와 ChatGPT 로그인 모드로 실행한다. 인증/API 키/기존 CODEX 설정 및 네트워크 프록시 환경 변수는 상속하지 않는다.
- 공식 CLI의 stdout/stderr는 인증 URL·state를 포함할 수 있어 폐기한다. 터미널에는 슬롯과 `launching`, `browser_waiting`, `importing`, `succeeded`, `failed`, `cancelled` 상태만 JSON으로 출력한다. `browser_waiting`은 프로세스 시작 후 대기 상태이며 브라우저 창 열림을 관측했다는 의미는 아니다.
- 로그인 대기는 최대 5분이다. 이는 Wiki 작성 제한 3분과 별개다. Ctrl+C/SIGTERM이나 시간 초과 시 CLI를 종료하고 임시 파일을 정리한다. 실패 시 재시도하지 않는다.
- 현재 adapter는 `auth_mode=chatgpt`와 `tokens`의 access/refresh/id token 및 account ID 구조를 지원한다. access token의 JWT 만료와 계정 claim 일치 여부를 검사하며, JWT 서명 검증이나 서버 인증 확인을 했다고 간주하지 않는다. 구조가 다르면 원문을 출력하지 않고 `login_credentials_unsupported`로 중단한다. 실제 0.153.4 브라우저 산출물의 호환성은 사용자 로그인 테스트 대상이다.
- 인증 파일은 os.Root 경계 안에서 symlink·hardlink·비정규 파일을 거절하고, 64 KiB 이하 및 소유자를 확인한다. 전용 디렉터리 안에서 파일 권한을 0600으로 제한하고 내용을 읽는다. Keychain 반영 전에 평문 auth 파일을 제거한다.
- macOS Security/CoreFoundation API를 cgo로 호출한다. 서비스 `com.bazzi.codex-switcher.accounts.v1`, account `registry`의 단일 generic-password 항목에 버전과 두 슬롯의 인증 자료를 저장한다. 실제 계정 ID는 Keychain 내부 데이터에만 들어가며 항목 이름에 사용하지 않는다. iCloud 동기화는 비활성화한다.
- 새 외부 Go 패키지는 추가하지 않는다. 대안인 `/usr/bin/security ... -w TOKEN`은 프로세스 인자 노출 위험 때문에 쓰지 않는다. native 코드 빌드에 Xcode Command Line Tools와 CGO가 필요하다. 다른 OS 또는 CGO 비활성 환경에서는 명시적으로 실패하며 파일 저장 fallback이 없다.
- 상태 조회는 슬롯 등록 여부와 만료 시각만 반환한다. `stored_unverified`는 저장됐다는 뜻이고 인증 정상·사용량 가용 상태를 뜻하지 않는다. 자동 토큰 갱신은 아직 구현하지 않는다.
- 기존 슬롯은 login으로 덮어쓰지 않는다. ADR 0026에 따라 명시적 reauth는 사용자 ID와 account ID가 모두 같은 경우에만 반영한다. 다른 슬롯의 같은 조합은 중복 등록하지 않으며 저장소 손상·접근 실패 시 초기화/덮어쓰기를 하지 않는다.

## 검증

- 합성 runner/vault 테스트: 정상 상태 전이, 재시작 후 상태 조회, 중복·세 번째 슬롯·잘못된 재인증 거절, 취소·CLI 실패·파싱 실패·저장 실패 시 임시 디렉터리 정리, symlink/hardlink 및 크기 제한, 출력 redaction.
- opt-in native 테스트: 무작위 이름의 별도 Keychain 항목에 합성 데이터만 생성·갱신·재조회하고 해당 항목을 삭제한다. 기존 Codex 또는 Switcher 계정 항목에 접근하지 않는다.

```sh
SWITCHER_KEYCHAIN_INTEGRATION=1 go test -count=1 -v -timeout 30s ./internal/credentialstore
```

## 한계 및 다음 단계

- 실제 브라우저 로그인, 실제 인증 자료 형식, 재빌드/코드 서명 변경 뒤 Keychain 접근 승인은 사용자 환경 검증이 필요하다. Keychain 접근 시 macOS 승인이 표시될 수 있다. Bundle ID 이름 자체가 접근 제어 경계는 아니며 실제 보안 API의 접근 제어가 적용된다.
- helper는 로그인 프로세스를 한 번만 실행한다. 공식 CLI 로그인 내부의 네트워크 재시도 동작은 아직 검증하지 않았으며, 모델 요청에 대한 무재시도 테스트 결과와 구분한다.
- 후속 ADR 0014에서 개발용 단일 계정 1회 live-test를 추가한다. 사용량 조회, token refresh single-flight, 일반 대화형 세션 영속화 및 Wiki 인계는 후속 작업이다.
- 브라우저가 자동으로 열리지 않으면 이 버전에서는 인증 URL을 터미널에 노출하는 fallback을 제공하지 않는다. 사용자가 취소 후 환경을 점검한다.
- 종료를 가로챌 수 없는 SIGKILL/전원 차단에서는 임시 로그인 디렉터리가 남을 수 있다. 완전한 디스크 보안 삭제나 메모리 내 모든 토큰 복사본 소거를 보장하지 않는다. 강제 종료 복구와 orphan 로그인 프로세스 정리는 후속 보안 작업이며 남은 파일을 진단 로그에 수집하지 않는다.
- Keychain 시스템 승인을 기다리는 네이티브 호출은 Go context로 즉시 취소되지 않을 수 있다. 저장 이후 취소가 도착한 경우 저장된 계정은 유지된다.

## 근거

- [공식 Codex 인증 문서](https://learn.chatgpt.com/docs/auth): 브라우저 로그인, CODEX_HOME의 file/keyring 저장.
- [Apple SecItemAdd](https://developer.apple.com/documentation/security/secitemadd(_:_:)), [SecItemCopyMatching](https://developer.apple.com/documentation/security/secitemcopymatching(_:_:)).
- 설치된 `codex-cli 0.153.4`의 `login --help` 및 macOS native 합성 테스트.
