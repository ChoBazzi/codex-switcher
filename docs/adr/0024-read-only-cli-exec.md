# ADR 0024: 읽기 전용 단발 CLI 실행 연결

- 상태: accepted
- 날짜: 2026-09-06
- 관련: ADR 0022, 0023

## 결정

후속 ADR 0025에서 실사용 exec의 기록 보존과 명시적 resume을 추가했다. 아래 ephemeral/재개 미지원 설명은 최초 구현 단계의 범위이며, 합성 임시 실행에는 계속 적용한다.

`switcher-helper exec [-C DIRECTORY] [--model MODEL] PROMPT`로 사용자가 명시한 새 작업을 실행한다. 대화형 TUI·resume·계정 전환을 제공한다고 해석하지 않는다.

- 로컬 origin → 계정별 사용량 → 영속 라우터 → CLI thread.started → 영속 프록시를 연결한다. 요청마다 origin을 다시 확인하고 바뀌면 거절한다.
- 사용량은 시작 시 한 번, 이후 기존 Monitor의 60초 정규 주기로 갱신한다. 계정 선택과 실패 시 동작은 기존 정책 그대로다. HTTP 또는 CLI 실패 후 자동 재전송하지 않는다.
- 별도 임시 CODEX_HOME과 전용 프로필을 사용한다. 기존 전역 설정/로그인을 복사하거나 수정하지 않는다. 기본 모델을 사용하며 명시적 모델 옵션을 지원한다. 설정 공유는 후속 작업이다.
- 현재 실행은 읽기 전용 sandbox, 승인 never, 웹 검색 비활성, ephemeral로 제한한다. 프롬프트는 자식 CLI argv 대신 stdin으로 전달한다. CLI stderr와 원본 JSON 이벤트는 진단 로그로 내보내지 않는다. 사용자에게는 agent_message와 내부 슬롯 a/b만 출력한다.
- 이벤트는 1MiB/줄로 제한한다. 시작 중복·등록 실패·이벤트 파싱 실패·turn.failed·불완전 종료는 실패이며 프로세스를 취소한다. 정상 종료에는 turn.completed 및 exit 0이 모두 필요하다. 부분 출력은 이미 표시될 수 있으며 성공 판정과 다르다.
- 실제 OAuth 토큰은 helper 안에서만 사용한다. CLI에는 loopback 프록시의 실행별 무작위 비밀값만 전달한다. 임시 실행 디렉터리는 종료 시 삭제하며 실패하면 정리 필요 오류를 반환한다.
- SQLite는 전용 affinity 디렉터리의 단일 소유 잠금을 사용한다. 현재 이 명령을 동시에 두 개 실행할 수 없다. 다중 CLI를 받는 상주 helper는 후속 작업이다. 계정 login/usage 명령은 기존 경로를 유지한다.

## 검증과 한계

### 실행 진단

`cli_run_finished`는 CLI 요청 수, upstream 전송 시도 수, 마지막 HTTP 상태, 로컬 거절 코드, 정규화된 CLI 단계 및 성공 여부를 stderr JSON으로 출력한다. 요청 처리 종료를 최대 3초 기다린 뒤 관측한다. HTTP 200이나 전송 시도 자체는 작업 완료를 의미하지 않는다. 프록시가 받은 upstream 오류 본문, CLI stderr/오류 메시지, 토큰과 실제 식별자는 기록하지 않는다. 진단은 메모리 카운터와 로컬 상수에서만 구성하며 원문에서 오류 코드를 추출하지 않는다. CLI 단계 분류와 로컬 거절·upstream 오류의 합성 비밀값 비노출을 테스트한다. 디스크 로그/순환 보관은 아직 추가하지 않았다.

공식 [비대화형 실행 문서](https://learn.chatgpt.com/docs/non-interactive-mode) 및 설치된 CLI 도움말을 확인했다. 합성 테스트는 실제 CLI → 새 실행기 → 세션 연결 → SQLite 프록시 경로의 정상·429·중단 응답, upstream 한 번 및 임시 파일 정리를 확인한다. 테스트 중 실제 모델 호출은 없다. loopback 권한을 허용한 환경에서 전체 race 테스트와 선택적 CLI 통합 테스트가 통과했으며 vet·빌드도 성공했다. 새 exec 명령의 실계정 검증은 사용자 확인 단계다.

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 ./internal/clirun
```

실계정 기본 모델의 전체 도구/이력 호환성, 파일 수정, resume 기록 보존, TUI, Wiki 전환, 토큰 갱신, 상주 helper/동시 CLI는 아직 미지원이다. 현재 exec 종료 시 CLI 기록을 제거하므로 이 명령으로 만든 대화를 resume할 수 없다. SQLite 소유권만 남으며 기존 GC 정책 대상이다. 강제 종료 시 임시 디렉터리 정리는 보장되지 않는다. 새 외부 의존성 없음.
