# Codex Switcher

macOS 메뉴바에서 여러 Codex 계정의 사용량과 한도를 확인하고, 현재 계정의 한도가 소진되면 로컬 역방향 프록시를 통해 사용 가능한 계정으로 안전하게 전환하는 도구입니다.

## Status

현재는 설계 및 프로토콜 검증 단계입니다. 구현 언어와 프로세스 구조를 결정한 뒤 단일 계정 프록시부터 개발합니다.

## Product principles

- Codex CLI 프로세스를 유지한 채 안전한 경우에만 계정을 전환합니다.
- 이미 시작된 스트리밍이나 중복 실행 가능성이 있는 요청은 자동 재시도하지 않습니다.
- OAuth 토큰과 인증 정보는 로그, 저장소 및 LLM Wiki에 기록하지 않습니다.
- 프록시와 제어 API는 로컬 loopback에서만 접근할 수 있어야 합니다.
- 계정 간 서버 세션 이전이 불가능할 때는 LLM Wiki 기반 Context Bridge를 사용합니다.

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
