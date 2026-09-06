# Codex Switcher

macOS 메뉴바에서 여러 Codex 계정의 사용량과 한도를 확인하고, 현재 계정의 한도가 소진되면 로컬 역방향 프록시를 통해 사용 가능한 계정으로 안전하게 전환하는 도구입니다.

## Status

현재는 Phase 0 구현 단계입니다. Go 기반 Wiki 로컬 검증, 메모리 기반 세션 인계, 단일 시도 HTTP/SSE 프록시와 합성 데모가 있습니다. 실제 CLI 0.153.4로 합성 응답·오류·대화 ID 격리를 검증했습니다. 브라우저 로그인과 Keychain 저장은 사용자 환경에서도 확인했으며, 등록한 단일 계정의 실제 요청을 확인하는 `live-test`를 추가했습니다. 실제 서버 응답은 사용자 검증 대상입니다. SwiftUI, 일반 대화형 CLI 연결, 사용량/토큰 갱신, SQLite 영속화, Codex Wiki 인계 연동은 아직 구현되지 않았습니다.

## Local development

Go 1.24 이상이 필요합니다. 외부 Go 패키지 의존성은 없습니다. macOS Keychain 빌드에는 Xcode Command Line Tools와 활성화된 CGO(기본값)가 필요합니다.

```sh
go test -race ./...
go vet ./...
go build -o bin/switcher-helper ./cmd/switcher-helper
```

제한된 실행 환경에서 기본 Go 캐시에 쓸 수 없으면 `GOCACHE=/private/tmp/codex-switcher-go-build`를 지정합니다. HTTP 통합 테스트는 loopback 포트를 열 수 있어야 합니다.

합성 데모 실행 (실제 Codex 설정·인증을 변경하지 않음):

```sh
export SWITCHER_CONTROL_TOKEN="local-demo-only-secret"
go run ./cmd/switcher-helper --demo
```

별도 터미널에서:

```sh
curl -N http://127.0.0.1:8765/responses \
  -H 'Content-Type: application/json' \
  -H 'X-Switcher-Demo-Session: demo' \
  -d '{"input":"synthetic"}'

curl http://127.0.0.1:8765/control/status \
  -H 'Authorization: Bearer local-demo-only-secret'
```

데모는 고정된 합성 SSE를 반환하며 실제 모델을 호출하지 않습니다. 데모 헤더를 공식 Codex 세션 식별 방식으로 사용하면 안 됩니다. 실패한 데모 세션은 프로세스 생명주기 동안 차단됩니다.

## 실제 CLI 연결 테스트 (합성 서버)

설치된 `codex` 실행 파일이 필요합니다. 검증 버전은 **0.153.4**입니다.

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v -timeout 100s ./internal/cliprobe
```

테스트가 자체적으로 임시 프로필과 로컬 서버를 준비하므로 별도 helper 실행은 필요하지 않습니다. 기존 Codex 설정과 로그인 정보를 변경하지 않으며 실제 모델을 호출하지 않습니다. 테스트용 임시 대화 기록은 종료 시 정리합니다.

- 정상 응답과 429·503·스트리밍 중단: CLI와 upstream 각각 요청 1회인지 검사.
- 두 작업 폴더에서 동시 실행: 대화 ID 분리 검사.
- 기존 대화 `exec resume`: ID 유지 검사. 같은 폴더에서 새 대화 시작: 새 ID 검사.
- 동일 TUI 프로세스 내 전환·Wiki 인계·실제 계정 전환은 아직 미검증입니다.

기존 curl 데모에서 오류를 수동 재현하려면 helper를 종료하고 시나리오를 지정해 다시 실행합니다.

```sh
SWITCHER_CONTROL_TOKEN=local-demo-only-secret \
  go run ./cmd/switcher-helper --demo --demo-scenario rate-limit
```

`success`, `rate-limit`, `server-error`, `partial`을 지원합니다. `/control/status`의 `upstream_calls`로 요청 횟수를 확인할 수 있습니다. 실패 후 같은 demo 세션을 다시 호출하면 409로 차단되며 횟수는 증가하지 않습니다.

자세한 검증 범위는 [ADR 0012](docs/adr/0012-installed-cli-synthetic-probe.md)를 참고하세요.

## 브라우저 계정 등록 테스트

아래 로그인 명령은 **실제 브라우저 인증을 시작하고 Switcher 전용 Keychain 항목에 저장**합니다. 기존 `~/.codex` 로그인 자료는 가져오거나 수정하지 않습니다. 모델 요청은 하지 않습니다.

```sh
go build -o bin/switcher-helper ./cmd/switcher-helper
./bin/switcher-helper account login a
```

브라우저에서 로그인한 뒤 `"state":"succeeded"`를 확인하세요. macOS Keychain 승인이 표시되면 helper 경로를 확인하세요. 로그인 취소는 `Ctrl+C`이며 최대 5분 대기합니다. 브라우저가 열리지 않을 경우 이 버전에서는 수동 인증 URL을 출력하지 않습니다.

새 터미널에서도 저장 상태를 확인할 수 있습니다.

```sh
./bin/switcher-helper account status
```

`registered: true`, `state: stored_unverified`는 로컬 저장 성공을 뜻하며 서버 인증 정상 여부나 사용량을 조회한 결과가 아닙니다. 만료된 계정은 `expired`로 표시합니다. 동일 계정으로 재인증하려면:

```sh
./bin/switcher-helper account reauth a
```

두 번째 계정은 `account login b`로 추가합니다. 같은 계정의 중복 등록과 잘못된 계정으로의 재인증은 거절합니다. 자동 계정 전환은 아직 테스트할 수 없습니다.

인증 관련 일반 테스트는 합성 데이터만 사용합니다. 네이티브 Keychain 저장을 따로 검사하려면:

```sh
SWITCHER_KEYCHAIN_INTEGRATION=1 go test -count=1 -v -timeout 30s ./internal/credentialstore
```

이 테스트는 고유 이름의 합성 항목만 만들고 종료 시 삭제합니다. 강제 종료 시 임시 인증 파일 잔류 등의 한계는 [ADR 0013](docs/adr/0013-browser-login-and-keychain.md)에 기록했습니다. 오류가 나면 **오류 코드만 공유하고 auth.json·토큰·인증 URL은 공유하지 마세요.**

## 실제 계정으로 인사 요청 1회 테스트

등록된 `a` 계정으로 테스트합니다. **이 명령은 실제 모델 요청을 보내 계정 사용량을 소모합니다.** 별도 helper 실행이나 전역 Codex 설정 변경은 필요하지 않습니다.

```sh
go build -o bin/switcher-helper ./cmd/switcher-helper
./bin/switcher-helper live-test a
```

내부에서 로컬 프록시와 새 CLI를 열어 “짧게 안녕이라고만 답해줘. 도구를 사용하지 마.”를 보냅니다. 답변과 함께 진단 JSON에 `succeeded: true`, `cli_requests: 1`, `proxy_admissions: 1`, `last_http_status: 200`이 나오면 성공입니다. 준비·실행·종료까지 자동으로 처리하며, 기존 대화나 프로젝트 작업을 이어가는 명령은 아닙니다.

모델을 생략하면 설치된 CLI의 기본 모델을 사용합니다. 기존 config의 모델 선택은 공유하지 않으므로 필요하면 `--model`로 계정에서 사용 가능한 모델명을 지정하세요.

`login_credentials_expired`가 나오면 사용자가 직접 `./bin/switcher-helper account reauth a`를 실행하세요. 실패 시 helper는 자동 재전송하지 않습니다. 재시험 명령을 다시 실행하는 것은 별개의 새 모델 요청입니다. 오류 공유 시 진단 JSON과 오류 코드만 보내고 원본 인증 자료는 보내지 마세요.

실제 토큰을 사용하지 않는 동일 경로의 합성 테스트:

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v -timeout 100s ./internal/livetest
```

지원 범위와 backend 경로의 미검증 사항은 [ADR 0014](docs/adr/0014-single-account-live-smoke-test.md)에 기록했습니다.

## Product principles

- 다음 세션에 계정을 지정하고 사용자 입력을 기다립니다. 같은 CLI에서 새 세션을 여는 연동은 검증 대상입니다.
- 실패한 요청은 같은 계정이나 다른 계정으로 자동 재시도하지 않습니다.
- OAuth 토큰과 인증 정보는 로그, 저장소 및 LLM Wiki에 기록하지 않습니다.
- 프록시와 제어 API는 로컬 loopback에서만 접근할 수 있어야 합니다.
- 현재 Codex가 잔여량 50%·10% 및 수동 전환 시 Wiki를 작성하고 다음 세션의 Codex가 참조합니다.

## Development roadmap

1. Codex 프로토콜과 OAuth 흐름 검증
2. 단일 계정 Responses/SSE 프록시
3. 계정별 사용량과 상태 관리
4. 두 계정 라우팅 및 session affinity
5. macOS 메뉴바 앱
6. LLM Wiki Context Bridge
7. 보안 강화, 코드 서명 및 배포

## Security

이 프로젝트는 인증 토큰을 다룹니다. 실제 계정 정보, OAuth 토큰, 세션 덤프 및 민감한 요청·응답 본문을 이 저장소에 커밋하지 마세요.
