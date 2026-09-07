# Codex Switcher

macOS 메뉴바에서 여러 Codex 계정의 사용량과 한도를 확인하고, 현재 계정의 한도가 소진되면 로컬 역방향 프록시를 통해 사용 가능한 계정으로 안전하게 전환하는 도구입니다.

## Status

현재는 Phase 0 구현 단계입니다. Go 기반 Wiki 로컬 검증, 메모리 기반 세션 인계, 단일 시도 HTTP/SSE 프록시와 합성 데모가 있습니다. 실제 CLI 0.153.4로 합성 응답·오류·대화 ID 격리를 검증했습니다. 브라우저 로그인·Keychain 저장, 실제 모델 응답과 사용량 조회는 사용자 환경에서도 확인했습니다. 읽기 전용 단발 `exec`에 프로젝트 식별·사용량 기반 계정 선택·SQLite 영속 프록시를 연결하고 합성 CLI 테스트를 통과했습니다. 이 실행 경로의 실계정 검증, SwiftUI, 일반 대화형 CLI 연결, 토큰 자동 갱신 및 Codex Wiki 인계 연동은 아직 남아 있습니다.

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

중복 판단은 사용자 ID와 워크스페이스(account) ID를 함께 비교합니다. 같은 Business 워크스페이스라도 사용자 ID가 다르면 등록을 허용합니다. `account_already_registered`는 두 값이 모두 같은 경우이며, `account_user_identity_unavailable`은 지원하는 사용자 claim이 없어 판단할 수 없는 경우입니다. 기존 A를 삭제하지 않고 B 로그인을 다시 검증할 수 있습니다. 실제 토큰 형식 호환성은 사용자 확인이 필요합니다. [ADR 0026](docs/adr/0026-user-and-workspace-identity.md)

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

`proxy_admissions: 0`이면 OpenAI 전송 전 로컬 검사에서 거절된 것입니다. 이 경우 `local_rejection_code`에서 구체적인 사유를 확인할 수 있습니다. CLI 기본 모델이 보내는 인라인 `additional_tools` 정의도 지원하며, 과거 대화 참조와는 구분합니다.

실제 토큰을 사용하지 않는 동일 경로의 합성 테스트:

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v -timeout 100s ./internal/livetest
```

지원 범위와 backend 경로의 호환성 제약은 [ADR 0014](docs/adr/0014-single-account-live-smoke-test.md)에 기록했습니다.

## 계정별 사용량 조회

저장된 인증으로 사용량 메타데이터만 읽습니다. **모델을 호출하지 않으며 대화나 Codex 설정을 변경하지 않습니다.** 저장소 폴더에서 실행하세요.

```sh
cd /Users/bazzi/dev/work/my
go build -o bin/switcher-helper ./cmd/switcher-helper
./bin/switcher-helper usage
```

`a`, `b`를 각각 조회하고 JSON 한 줄로 출력합니다. 특정 계정만 조회하려면 `usage a`를 사용하세요. 1분 간격으로 계속 확인하려면:

```sh
./bin/switcher-helper usage --watch
```

즉시 한 번 조회한 다음 60초마다 갱신합니다. `Ctrl+C`로 종료합니다. `usage --watch a`도 가능합니다. 옵션은 계정명 앞에 둡니다. 수동 갱신은 `usage`를 다시 실행하면 됩니다.

- `usage.primary`, `usage.secondary`: 서버가 알려준 사용률·잔여율·구간 길이(초)·초기화 시각(UTC). 확인되지 않은 값은 `null`입니다.
- `usage.remaining_percent`: 두 구간 잔여율의 최솟값. 한쪽이라도 모르면 `null`입니다. 절대 메시지 개수나 모델별 사용 가능 여부를 의미하지 않습니다.
- `state`: `ok`(두 구간 수치 확인), `unknown`, `limit_reached`(서버 명시), `not_registered`, `auth_expired`, `auth_error`, `rate_limited`, `fetch_error`.
- `last_attempt`, `last_success`, `stale`: 마지막 시도/성공 시각과 오래된 데이터 여부. watch 중 실패하면 이전 성공 수치를 유지하되 `stale: true`로 표시합니다. 수신이 멈춘 경우 소비자는 성공 시각에서 2분이 지나면 오래된 값으로 취급해야 합니다.
- `usage.checkpoint_level`: 잔여율 50%·10% 경계의 **관측값**입니다. 아직 Wiki 작성 이벤트나 자동 전환을 실행하지 않습니다. 이전 수치가 남아 있을 수 있으므로 `stale`과 함께 확인하세요.

401/403 또는 `auth_expired`이면 해당 계정을 `account reauth a` 또는 `account reauth b`로 직접 재인증하세요. 429는 조회 제한이며 계정 한도 소진으로 단정하지 않습니다. 오류 직후 추가 재시도는 없고, watch의 다음 정규 주기만 진행합니다. 단발 조회는 실패 상태를 출력한 뒤 종료 코드 1을 반환합니다(미등록·미확인 수치 자체는 오류 아님).

사용량 경로는 공개 API 계약이 아닌 Codex backend 호환 구현이며, 이번 추가분은 합성 응답으로 검증했습니다. 실제 계정의 응답 호환성은 위 명령으로 확인하세요. macOS Keychain 접근 승인이 표시될 수 있습니다. 토큰·계정 식별자·원본 서버 본문은 출력하지 않습니다. 앱/제어 API 연결과 영속 저장은 후속 단계입니다. [ADR 0015](docs/adr/0015-account-usage-polling.md)

## 내부 계정 선택·세션 고정 검증

사용량 실계정 조회 성공 후, 신규 계정 선택 모듈을 추가했습니다. 신규 세션은 기본 90% 소진 미만 계정 중 잔여율이 높은 계정을 선택하고 기존 세션은 원래 계정을 유지합니다. 사용량 미확인·조회 실패·인증 만료 시 다른 계정으로 우회하지 않습니다.

```sh
go test -race -count=1 ./internal/routing
```

이 명령은 합성 데이터와 로컬 프록시로 검증하며 모델을 호출하지 않습니다. 아직 일반 CLI/상주 helper에 연결하지 않은 내부 모듈입니다. 실제 프록시 continuation 연동과 Wiki 전환 준비는 후속 작업입니다. [ADR 0016](docs/adr/0016-new-session-account-selection.md)

## SQLite 세션 저장 검증

macOS 시스템 SQLite(CGO)를 사용하는 내부 저장소와 `routing.NewPersistent`를 추가했습니다. 세션 계정 고정, 응답 ID 소유권, 종료된 요청 의도의 차단 복구 및 14일 초과 유휴 정리를 검증합니다.

```sh
cd /Users/bazzi/dev/work/my
go test -race -count=1 ./internal/affinity ./internal/routing
```

합성 데이터와 임시 DB만 사용하며 로그인이나 모델 호출은 없습니다. [ADR 0017](docs/adr/0017-sqlite-affinity-storage.md)

선택적 `proxy.NewPersistent`는 요청 전 SQLite 기록, `previous_response_id` 소유권 검사, JSON/SSE 완료 ID 저장 및 실패 차단을 연결합니다. 텍스트와 소유권이 확인된 item 참조·함수 호출·문자열 함수 결과를 지원합니다. 기타 CLI 도구 확장은 아직 차단합니다. 기존 demo/live-test와 일반 CLI 런처에는 아직 적용하지 않았습니다. 실사용 자동 복구 기능은 아닙니다. [ADR 0018](docs/adr/0018-persistent-proxy-lifecycle.md), [ADR 0019](docs/adr/0019-function-and-item-ownership.md)

```sh
go test -race -count=1 ./internal/proxy ./internal/affinity ./internal/routing
```

설치된 CLI의 함수 결과 반환 형식만 따로 검증하려면 아래 명령을 사용합니다. 존재하지 않는 합성 도구를 사용하므로 실제 도구/모델 호출은 없습니다. 영속 프록시 전체 CLI 연결 테스트는 아닙니다.

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 ./internal/cliprobe -run TestInstalledCodexFunctionOutput
```

영속 프록시를 거치는 CLI 합성 함수 왕복 테스트도 추가했습니다. 새 메시지/함수 결과 ID의 소유권을 원자적으로 등록하며, CLI 시작 이벤트로 확인한 세션만 연결합니다. 실제 모델·도구를 호출하지 않는 개발용 검증이며 일반 CLI 런처 기능은 아닙니다. [ADR 0020](docs/adr/0020-cli-persistent-function-probe.md)

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v ./internal/cliprobe -run TestInstalledCodexPersistentFunction
```

영속 경로는 완전한 `additional_tools` 정의와 이전 완료 응답에서 관찰한 assistant/reasoning 이력도 검사합니다. 이력은 세션별 내용 해시와 item ID 소유권을 확인하며 변경된 내용은 차단합니다. 이 확장의 실제 CLI 왕복 검증은 남아 있고, 현재 로컬 합성 테스트로 검증합니다. [ADR 0021](docs/adr/0021-owned-history-and-tool-definitions.md)

CLI 프로세스별 연결 모듈도 추가했습니다. 실행별 인증, 시작 이벤트, 새 대화/resume 구분과 프로젝트 일치를 확인하며, 위 영속 함수 왕복 테스트가 이 모듈과 라우터를 함께 검증합니다. 일반 CLI 실행 명령은 아직 후속 작업입니다. [ADR 0022](docs/adr/0022-cli-process-session-binding.md)

## 로컬 프로젝트 식별

실사용 런처 준비를 위한 로컬 프로젝트 식별 명령을 추가했습니다. 경로·브랜치 원문 대신 프로젝트/worktree/브랜치 해시를 출력하며 세션 생성이나 모델 호출은 하지 않습니다. Git이 없는 경우 지정한 폴더 자체가 프로젝트 루트입니다. [ADR 0023](docs/adr/0023-local-project-identity.md)

```sh
cd /Users/bazzi/dev/work/my
go build -o bin/switcher-helper ./cmd/switcher-helper
./bin/switcher-helper project .
```

## 읽기 전용 CLI 작업 실행 (실계정)

등록한 계정의 사용량을 조회하고 새 세션을 선택한 계정에 고정하여 사용자 프롬프트를 실행합니다. **실제 모델 호출로 사용량을 소모합니다.** 최초 검증은 도구 없는 짧은 인사로 진행하세요.

```sh
cd /Users/bazzi/dev/work/my
go build -o bin/switcher-helper ./cmd/switcher-helper
./bin/switcher-helper exec -C . '짧게 안녕이라고만 답해줘. 도구를 사용하지 마.'
```

`cli_session_started`의 `slot`은 선택된 a/b 계정입니다. 답변 출력과 종료 코드 0을 확인합니다. 시작 이벤트만으로 모델 성공을 의미하지 않습니다. 필요하면 프롬프트 앞에 `--model MODEL`을 추가합니다. 오류가 나면 오류 코드만 공유하고 인증 자료는 공유하지 마세요.

종료 시 `cli_run_finished` 진단 JSON을 출력합니다. `upstream_attempts: 0`이면 모델 서버 전송 전 실패이며 `local_rejection_code`를 확인합니다. 전송 시도가 있으면 `last_http_status`와 `cli_stage`를 함께 확인하세요. 상태 200만으로 성공을 뜻하지 않습니다. 원본 오류·토큰·대화 내용은 진단에 포함하지 않습니다.

HTTP 200 이후 실패는 `response_failure_code`로 구분합니다. `upstream_response_failed`는 서버 실패 이벤트, `completion_shape_invalid`는 완료 응답 형식 불일치, `response_read_canceled`는 읽기 취소, `completion_store_conflict`는 영속 저장 충돌입니다. `completion_seen`은 완료 이벤트를 봤는지, `completion_committed`는 소유권 저장이 끝났는지를 나타냅니다. 실패 시 이 진단 한 줄만 공유하세요.

형식 오류는 `response_format`(sse/json/other/missing/invalid)과 `completion_rejection_code`도 확인합니다. 예를 들어 `completion_json_invalid`는 JSON 문법 또는 중복 키 오류, `completion_error_present`는 오류 객체 포함, `completion_status_invalid`는 완료 상태 불일치입니다. 원문 응답이나 토큰을 공유할 필요가 없습니다. 이 진단만으로 서버 오류 메시지의 구체적인 원인까지 알 수 있는 것은 아닙니다.

헤더 없는 성공 응답이 SSE 접두사로 시작하면 기존 SSE 검증 경로로 처리하며 `response_format=missing_sse`로 표시합니다. 본문을 수정하거나 실패 요청을 다시 보내지는 않습니다.

대화 메타데이터는 저장 후 파일을 다시 읽어 일치 여부를 확인합니다. `cli_record_write_verification_failed`는 이 검증의 실패이며 정상 완료로 표시하지 않습니다. 기존 재개 불가 기록을 자동으로 해제하지 않습니다.

SSE 이력 소유권은 `response.output_item.done`과 최종 완료 응답에서 함께 수집하고 전체 응답 완료 후 커밋합니다. 마지막 완료 이벤트의 output이 비어 있어도 앞서 검증한 항목을 누락하지 않습니다. 이력 해시 생성 불가는 `completion_history_invalid`로 표시합니다. 이 보완은 같은 계정의 명시적 재개를 위한 것이며 Wiki 기반 새 계정 인계를 대신하지 않습니다.

CLI 재개 시 생략되는 빈 `annotations`와 `logprobs` 배열은 이력 해시에서 정규화합니다. 값이 있는 배열과 원문 변경은 계속 구분합니다. 수정 전 해시나 실패 기록은 자동 변환하지 않으므로 재개 검증에는 새 대화를 사용합니다.

영속 프록시는 서버 EOF 검증과 SQLite 커밋 후 최종 완료 이벤트를 전달합니다. 완료 직후 CLI 연결 종료로 저장 전 차단되는 경합을 보완했습니다. 기존 실패 기록은 자동 해제하지 않으므로 해당 기록 대신 새 대화로 검증해야 합니다.

사용량은 즉시 및 60초마다 갱신합니다. 기존 Codex 설정·로그인은 변경하지 않으며 설정 공유는 아직 없습니다. 현재는 읽기 전용이며 TUI·동시 실행·자동 계정 전환은 지원하지 않습니다. 도구 호출 호환성은 추가 검증 중이며 실패 요청은 재전송하지 않습니다. [ADR 0024](docs/adr/0024-read-only-cli-exec.md)

### 같은 대화 재개

이 버전부터 CLI 대화를 Switcher 전용 Application Support 폴더에 보존합니다. 대화 내용이 로컬에 남으며 아직 자동 삭제 기능은 없습니다. 새 실행의 `cli_session_started`에 표시된 `conversation` 값(32자리)을 사용하세요.

```sh
./bin/switcher-helper exec -C . --resume CONVERSATION_HANDLE '방금 대화 내용을 바탕으로 답해줘. 도구는 사용하지 마.'
```

`CONVERSATION_HANDLE`은 실제 출력된 값으로 교체합니다. 같은 프로젝트·worktree·브랜치와 원래 계정으로만 재개합니다. 정상 완료한 대화만 가능하며 중단·실패·알 수 없는 기록은 차단합니다. 프롬프트 없는 자동 재개, `--last` 추정, resume 시 모델 변경은 지원하지 않습니다. 이전 ephemeral 버전에서 삭제된 대화는 재개할 수 없습니다. [ADR 0025](docs/adr/0025-explicit-cli-resume.md)

실제 모델을 호출하지 않는 실행 경로 검증:

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 ./internal/clirun
```

### 현재 대화에서 Wiki 작성

정상 완료한 대화의 Codex에게 Wiki를 작성하도록 명시적으로 요청합니다. **기존 계정의 모델 사용량을 소모합니다.** 프로젝트 루트에서 실행하고 HANDLE을 실제 대화 ID로 바꾸세요.

```sh
./bin/switcher-helper exec -C . --resume HANDLE --checkpoint
```

읽기 전용 CLI가 Markdown을 생성하면 helper가 요청 ID·원본·필수 항목을 검증하고 `.codex-switcher/<worktree>/<branch>/<session>/checkpoint.md`에 저장합니다. 본문은 터미널에 출력하지 않습니다. 실제 전송부터 검증까지 최대 3분이며 자동 재시도는 없습니다. `cli_run_finished`만으로 저장 성공을 판단하지 말고 별도의 `checkpoint_saved` 이벤트를 확인하세요. `switch_ready:false`는 정상입니다. 계정 전환이나 다음 모델 요청은 시작하지 않습니다.

저장 성공 시 전역 경로 `~/.codex/switcher/projects/<project>/<worktree>/<branch>/<session>/snapshots.json`에 최근 3개도 보관하며 `backup_saved:true`를 출력합니다. 대상 계정 연결과 메뉴바 UI는 아직 미연결입니다. [ADR 0027](docs/adr/0027-cli-wiki-draft-publication.md), [ADR 0028](docs/adr/0028-wiki-backups-and-explicit-recheck.md)

생성 종료 시 `checkpoint_candidate`에 표시된 checkpoint ID로 저장 상태를 재확인할 수 있습니다. **모델 호출·사용량 조회·타이머 재시작 없이** 보존된 후보 파일을 한 번 검증하고 로컬/전역에 게시합니다.

```sh
./bin/switcher-helper checkpoint recheck -C . --conversation HANDLE --id CHECKPOINT_ID
```

`checkpoint_rechecked`, `backup_saved:true`가 성공 기준입니다. 취소된 요청, 새 요청으로 대체된 ID, 다른 프로젝트는 거절합니다. 후보가 불완전하면 재확인만으로 내용이 완성되지는 않습니다. 후보는 해당 conversation의 private CLI home 내 `wiki-candidate.md`이며 수정하더라도 메타데이터와 종료 마커를 유지해야 합니다. 재확인은 실패한 대화를 재개 가능으로 바꾸거나 계정 전환을 실행하지 않습니다.

## Product principles

### 명시적 Wiki 계정 인계

먼저 원본 대화의 `--checkpoint`를 실행해 저장 성공을 확인하세요. 원본이 A라면 B로 예약합니다. HANDLE은 원본 대화 ID, CHECKPOINT_ID는 저장 이벤트의 ID입니다. `--confirm-boundary`는 미해결 작업이 없고 Wiki가 최신 상태임을 사용자가 확인하는 옵션입니다.

```sh
./bin/switcher-helper handoff prepare -C . --conversation HANDLE --checkpoint CHECKPOINT_ID --to b --confirm-boundary
```

이 명령은 대상 사용량 메타데이터를 조회하지만 모델을 호출하지 않습니다. `handoff_prepared`의 `handoff_id`를 복사하세요. 원본 작업은 예약 중 보류됩니다. 새 사용자 입력으로만 인계를 소비합니다. **아래 exec는 실제 모델 사용량을 소모합니다.**

```sh
./bin/switcher-helper exec -C . --handoff HANDOFF_ID 'Wiki의 현재 상태를 짧게 설명해줘. 도구는 사용하지 마.'
```

새 conversation과 대상 slot, `succeeded:true`를 확인하세요. 새 CLI에는 이전 대화가 아닌 고정 Wiki 본문과 새 입력만 전달됩니다. 같은 예약은 한 번만 연결할 수 있으며 실행 실패 후 자동 재전송하지 않습니다. `--resume`과 함께 사용하지 마세요. 고정/소비된 checkpoint의 재확인은 차단됩니다.

사용 전 예약을 취소하고 원본 작업으로 돌아가려면 다음을 실행하세요. 모델/사용량 조회 없이 예약만 제거하고 Wiki와 고정 파일은 보존합니다.

```sh
./bin/switcher-helper handoff cancel -C . --conversation HANDLE --id HANDOFF_ID
```

일반 대화형 TUI를 계속 켜둔 채 전환하는 기능과 메뉴바 UI는 아직 없습니다.

영속 Wiki 인계의 저장소·라우터·CLI 시작 이벤트 바인딩을 연결했습니다. 예약 복원, 한 번만 소비, 대상 계정 재검사와 원본 continuation 격리를 합성 테스트로 검증합니다. [ADR 0029](docs/adr/0029-persistent-handoff-binding.md), [ADR 0030](docs/adr/0030-cli-explicit-handoff.md)

```sh
go test -race -count=1 ./internal/affinity ./internal/routing ./internal/clisession
```

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
