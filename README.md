# Codex Switcher

macOS 메뉴바에서 여러 Codex 계정의 사용량과 한도를 확인하고, 현재 계정의 한도가 소진되면 로컬 역방향 프록시를 통해 사용 가능한 계정으로 안전하게 전환하는 도구입니다.

## Status

현재는 Phase 0 구현 단계입니다. Go 기반 Wiki 로컬 검증, 메모리 기반 세션 인계, 단일 시도 HTTP/SSE 프록시와 합성 데모가 있습니다. SwiftUI, 실제 OAuth/Keychain, 사용량 조회, SQLite 영속화, Codex 프롬프트·세션 연동은 아직 구현되지 않았습니다.

## Local development

Go 1.24 이상이 필요합니다. 외부 Go 의존성은 없습니다.

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
