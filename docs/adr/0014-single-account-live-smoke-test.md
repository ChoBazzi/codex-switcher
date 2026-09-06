# 0014. 단일 계정 실제 요청 smoke test

- 상태: accepted (개발용 1회 요청 범위)
- 날짜: 2026-09-06
- 관련: ADR 0002, 0003, 0009, 0012, 0013

## 결정 배경

사용자 환경에서 브라우저 로그인과 Keychain 저장이 확인됐다. 다음 단계로 기존 계정을 가져오거나 프로젝트 대화를 섞지 않고 등록한 슬롯의 실제 모델 응답을 확인해야 한다.

## 구현 결정

- `switcher-helper live-test a|b [--model MODEL]`은 새 loopback 서버와 격리 CLI를 준비한 뒤 고정 인사 질문 1회를 전송한다. 이 명령을 실행하면 실제 계정 사용량이 소모된다. 개발 과정의 자동 테스트에서는 합성 토큰·서버만 사용한다.
- Keychain에서 지정 슬롯의 access token과 account ID만 모델 경로에 전달한다. refresh/ID token은 CLI와 프록시에 전달하지 않는다. 만료까지 30초 이하이면 요청 전에 거절하고 명시적 재인증을 안내한다. 자동 refresh나 다른 계정 대체는 하지 않는다.
- 계정 작업 잠금을 실행 동안 유지한다. 별도 프로세스의 로그인·재인증과 동시에 인증 상태를 교체하지 않는다. 테스트 동안 account status도 busy일 수 있다.
- 임시 0700 디렉터리에 로컬 실행용 profile만 생성한다. 실제 OAuth 토큰은 파일·환경 변수·프로세스 인자로 전달하지 않는다. CLI는 새 대화, ephemeral, read-only로 실행하고 기존 사용자 config/auth/대화 자료를 복사하지 않는다.
- 기존 모델 설정은 공유하지 않는다. `--model` 생략 시 설치된 CLI의 기본 모델을 사용하며 해당 계정의 모델 접근 권한을 보장하지 않는다. 필요하면 사용자가 이용 가능한 모델명을 명시한다.
- 자식 CLI의 provider 목적지는 숫자 loopback 주소로 제한한다. 공식 설정의 `request_max_retries=0`, `stream_max_retries=0`, `supports_websockets=false`를 사용한다. CLI의 다른 HTTP(S) 트래픽은 dead loopback proxy를 지정해 제한하며 OS 수준 egress 격리를 보장한다고 설명하지 않는다.
- helper의 실제 목적지는 `https://chatgpt.com/backend-api/codex/responses`로 고정한다. 사용자 입력 URL이나 환경 변수로 변경할 수 없고 redirect는 추적하지 않는다. 이 경로는 설치된 CLI 0.153.4 바이너리의 backend base URL과 합성 테스트로 확인한 Responses 경로를 조합한 호환성 가정이며, 공개 API 계약이나 실제 서버 검증 완료로 간주하지 않는다.
- 실행마다 생성한 로컬 식별 secret과 CLI 대화 헤더를 확인한다. 로컬 secret은 upstream에 전달하지 않는다. 테스트 실행 전체에 1회 예산을 적용하므로 실패 후 다른 thread ID를 보내도 추가 요청이 통과하지 않는다.
- 기존 continuation/conversation 필드 및 assistant/tool/item-reference 입력은 거절한다. 이는 새 인사 대화 전용 제한이고 장기 작업/도구 루프/세션 재개 API가 아니다.
- upstream 본문은 기존 단일 시도 프록시로 전달한다. installed CLI 합성 테스트에서 `stream=true`, `store=false`를 확인한다. 실패 응답의 재생, 프롬프트 변경 및 토큰 갱신은 하지 않는다.
- CLI 원본 stdout/stderr는 제한된 메모리 버퍼에만 받고 실패 원문을 출력하지 않는다. 성공한 assistant 최종 메시지는 사용자 응답으로 stdout에 표시한다. 별도 진단(stderr)은 슬롯·요청 수·HTTP 상태·성공 여부만 포함한다.
- `proxy_admissions`는 프록시에 진입한 횟수이며 서버 실행 완료 횟수는 아니다. CLI 요청·진입 각각 1회, HTTP 2xx 및 CLI 성공 응답이 함께 확인돼야 테스트 성공으로 표시한다.
- 최대 2분, Ctrl+C/SIGTERM 종료를 지원하며 정상 종료에서는 임시 CLI 디렉터리와 서버를 정리한다. 1회 실행 후 CLI 대화를 보존하거나 자동 재개하지 않는다.

## 검증 결과 및 재현

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v -timeout 100s ./internal/livetest
```

- 실제 CLI와 합성 upstream: 정상 최종 응답, HTTP 429/503 및 부분 SSE 종료의 호출 수 각각 1회.
- 선택한 합성 계정의 Authorization/계정 헤더 삽입 및 로컬 secret 제거.
- 다른 대화 ID의 추가 요청 차단, 잘못된 실행 식별자·대화 헤더·만료된 인증·continuation 거절.
- 사용하지 않는 슬롯, 만료 자격 증명, 출력 크기 제한 및 임시 디렉터리 정리 검증.

## 한계

실제 서버 응답은 사용자가 명령을 실행해 확인해야 한다. 데이터 레지던시를 요구하는 workspace, 장기 세션, 프로젝트 설정 공유, 토큰 갱신, 사용량 조회, 자동 전환, Wiki 인계 및 일반 대화형 CLI 연결은 이 명령의 지원 범위가 아니다. 요청 실패 시 원인에 따라 인증/모델/프로토콜을 확인하되 자동 재전송하지 않는다. 테스트 응답은 프로젝트 작업에 사용하지 않는다.

## 근거

- [공식 Configuration Reference](https://learn.chatgpt.com/docs/config-file/config-reference)
- [공식 Advanced Configuration](https://learn.chatgpt.com/docs/config-file/config-advanced)
- 설치된 CLI 0.153.4의 바이너리 상수 및 합성 통합 테스트.
