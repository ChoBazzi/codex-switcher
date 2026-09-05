# 0012. 실제 Codex CLI의 합성 프록시 호환성 검증

- 상태: accepted (합성 테스트 범위)
- 날짜: 2026-09-06
- 검증 버전: `codex-cli 0.153.4`, macOS arm64
- 관련: ADR 0002, 0003, 0009, 0011

## 결정 배경

curl 데모 통과는 실제 CLI의 설정 로딩, 세션 헤더, SSE 파싱 및 재시도 정책을 보장하지 않는다. 실제 계정 연결 전에 설치된 실행 파일을 합성 서버와 함께 검증한다.

## 선택과 구현

- Go 표준 라이브러리 기반 opt-in 통합 테스트를 추가한다. 외부 의존성 및 실제 모델 호출은 없다.
- 임시 `CODEX_HOME`에 `switcher-probe.config.toml`을 생성하고 `codex exec --profile switcher-probe --strict-config`로 실행한다. 기존 사용자 설정과 인증 파일을 복사하거나 변경하지 않는다. 임시 데이터는 테스트 종료 시 정리된다.
- `--ignore-user-config`는 사용하지 않는다. 검증 버전에서 이 옵션과 profile을 함께 사용하면 전용 설정이 적용되지 않고 기본 전송 경로를 시도했다. 설정 격리는 임시 디렉터리로 수행한다.
- 테스트 전용 provider는 숫자 loopback HTTP 주소만 허용한다. `wire_api="responses"`, `requires_openai_auth=false`, `supports_websockets=false`, `request_max_retries=0`, `stream_max_retries=0`을 지정한다. 이는 실제 OAuth provider 설계를 교체하는 결정이 아니다.
- 자식 CLI 환경에 인증/API 키/기존 CODEX 설정 변수를 상속하지 않는다. 기존 HOME 경로는 유지한다. HTTP(S) 프록시는 사용하지 않는 loopback 포트로 지정하고 로컬 서버만 예외로 둔다. 이것은 OS 수준의 완전한 egress 차단을 보장하지 않는다.
- 합성 응답에는 도구 호출이 없고 CLI는 read-only sandbox로 실행한다. CLI 출력의 원문과 요청 헤더 값은 테스트 로그에 출력하지 않는다.
- 테스트 요청은 `POST /responses`, HTTP/SSE다. 검증 버전에서 `Thread-Id`와 `Session-Id`는 JSONL `thread.started.thread_id`와 일치한다. 누락·중복·불일치·잘못된 UUID는 거절한다. 헤더는 인증 정보가 아니며 버전 변경 시 다시 검증한다.
- 기존 curl 데모는 식별 헤더가 없으면 고정 demo 세션을 사용한다. CLI 헤더가 하나라도 있으면 엄격하게 검증하고, 실패 시 고정 세션으로 대체하지 않는다. 이 예외는 합성 데모에만 적용한다.

## 확인한 결과

다음 명령으로 로컬에서 재현한다.

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v -timeout 100s ./internal/cliprobe
```

- 정상 SSE를 CLI가 최종 assistant 메시지로 처리한다.
- HTTP 429, 503, 완료 이벤트 전 스트림 중단에서 CLI 요청과 upstream 요청은 각각 1회이고 CLI는 실패로 종료한다.
- 두 작업 폴더의 동시 새 대화는 다른 ID를 사용한다. 명시적 `exec resume`은 원래 ID를 유지하고 같은 폴더의 새 `exec`는 새 ID를 발급한다. 요청 수는 세 대화에 각각 2/1/1이다.
- 프로필과 프록시 준비 자체는 모델 요청을 발생시키지 않는다.

## 미검증 및 한계

- 실제 ChatGPT OAuth, 한도/사용량 API, Keychain과 SQLite는 연결하지 않았다.
- 대화 헤더만으로 프로젝트 해시·worktree·브랜치를 확정하지 않는다. 현재 결과는 대화 격리 검증이며 프로젝트 등록 및 Wiki 인계 어댑터 완성을 의미하지 않는다.
- 동일 TUI 프로세스의 새 대화 시작, 사용자 입력 감지, Wiki 지시 주입 및 다음 계정 연결은 여전히 검증 대상이다. `exec resume` 검증을 같은 터미널 내 무중단 전환 보장으로 해석하지 않는다.
- 테스트 프로필의 재시도 0 및 관측 결과는 해당 버전/시나리오에 한정된다. 프록시 자체의 실패 세션 차단을 유지한다.

## 근거

- [공식 Configuration Reference](https://learn.chatgpt.com/docs/config-file/config-reference): provider 전송 및 재시도 설정.
- [공식 Advanced Configuration](https://learn.chatgpt.com/docs/config-file/config-advanced): 독립 profile 파일과 CODEX_HOME.
- 로컬 `codex exec --help`, `codex exec resume --help` 및 합성 실행 결과.
