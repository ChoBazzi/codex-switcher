# ADR 0025: 대화 기록 보존과 명시적 CLI 재개

- 상태: accepted
- 날짜: 2026-09-06
- 관련: ADR 0021, 0022, 0024

## 결정

사용자 확인으로 단발 실제 모델 응답 경로를 확인한 뒤 명시적 `exec --resume HANDLE PROMPT`를 추가한다. ADR 0024의 실사용 exec ephemeral 정책을 대화별 비공개 기록 보존으로 변경한다. 기존 임시 실행으로 삭제된 대화는 복원하지 않는다.

- 새 exec는 Application Support의 Switcher 전용 `conversations/<무작위 128비트 hex>/`에 CLI 홈과 메타데이터를 보존한다. 디렉터리는 0700, helper 메타데이터는 0600이며 소유자 및 잠금을 검사한다. 대화 본문이 저장되므로 사용자가 민감한 내용을 입력하면 이 폴더에도 남는다. 로그나 Wiki로 복사하지 않는다.
- 사용자에게는 로컬 conversation 핸들만 출력한다. 내부 메타데이터의 실제 CLI thread ID는 출력하지 않는다. 핸들을 경로로 쓰기 전에 정확한 소문자 hex 길이를 검증하며 알 수 없는 핸들은 생성하지 않는다.
- 재개 전에 origin 및 SQLite 소유권/ready 상태, 사용량·인증을 확인한다. CLI 시작 이벤트도 기존 ID와 일치해야 한다. 원래 계정만 사용하며 새 세션이나 다른 계정으로 대체하지 않는다.
- 실행 직전에 기록을 재개 불가로 원자 저장하고, CLI 정상 종료와 영속 프록시 ready 확인 후에만 다시 재개 가능으로 저장한다. 실패·강제 종료·기록 오류는 재개 불가로 남으며 자동 복구/재전송하지 않는다.
- 모델 옵션은 최초 지정값을 유지한다. resume과 모델 변경을 함께 받지 않는다. 처음 모델을 생략했다면 CLI 기본 모델을 따르므로 CLI 업그레이드에 따른 기본값 변경까지 고정하지는 않는다.
- 실행별 profile은 임시 파일에서 원자 교체하고 종료 시 삭제한다. CLI 홈은 보존하되 OAuth 토큰은 넣지 않는다. 테스트에서는 CLI가 남긴 파일에 합성 실행 비밀값이 없는지도 확인한다. 비정상 강제 종료의 잔여 profile 정리는 후속 운영 작업이다.
- CLI resume 직렬화에서 빈 `annotations: []` 및 `logprobs: []`가 생략된다. assistant 내용 해시는 이 빈 배열만 정규화한다. 비어 있지 않은 값, null 및 실제 텍스트 변경은 그대로 구분한다. 이전 해시를 무조건 허용하거나 외부 세션 이력을 새 소유권으로 등록하지 않는다.

## 검증 및 한계

### 빈 logprobs 정규화 (2026-09-07)

출력 이벤트 수집 보완 후 실계정 DB에 history 1개가 저장됐지만 재개가 계속 409로 거절됐다. 원문/ID를 출력하지 않고 로컬 CLI 이력의 해시를 비교한 결과, 항목 ID는 소유권이 일치했고 content에 logprobs=[]를 복원했을 때만 저장된 해시와 일치했다. output_text의 빈 logprobs 배열을 생략과 동등하게 정규화하여 해결한다. 비어 있지 않은 값이나 null을 무시하지 않는다. 합성 upstream에서 빈 logprobs 및 빈 최종 output을 함께 제공하는 실제 CLI 재개 회귀 테스트를 추가한다. 기존 실패 기록과 구형 해시는 자동 수정하지 않으며 새 대화로 검증한다.

### 저장 확인 보완 (2026-09-07)

실사용에서 succeeded=true 로그 후 첫 resume이 실패하고 record.json의 ready=false가 관찰됐다. 현재 합성 테스트에서는 재현되지 않아 원인은 미확정이다. 메타데이터 저장은 파일 sync·원자 rename·디렉터리 sync 이후 다음 프로세스가 사용하는 경로를 다시 열어 직렬화한 바이트와 일치하는지 확인한다. 불일치는 `cli_record_write_verification_failed`로 실패하며 성공 로그를 출력하지 않는다. 검증 이후의 외부 변경까지 방지하거나 기존 blocked/ready=false 기록을 복구하는 기능은 아니다. 완료 직후 디스크 ready=true 및 첫 Load/실패한 origin 검증이 파일을 변경하지 않음을 회귀 테스트한다.

[공식 비대화형 CLI 문서](https://learn.chatgpt.com/docs/non-interactive-mode)의 명시적 session ID resume 및 설치된 CLI 옵션을 확인했다. 합성 테스트에서 새 CLI/프록시를 종료하고 SQLite와 라우터를 다시 열어 동일 계정/세션의 재개를 검증한다. 알 수 없는 핸들·다른 브랜치·중단 기록·중복 잠금은 로컬 테스트로 거절한다.

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 ./internal/clirecord ./internal/clirun ./internal/proxy
```

읽기 전용, 한 번에 한 CLI, 실패 재전송 금지는 유지한다. TUI·Wiki 계정 전환·파일 수정은 아직 지원하지 않는다. 실계정의 다중 턴/도구/암호화 reasoning 이력 호환성은 추가 검증 대상이다. CLI 기록 삭제/보관 정책과 SQLite GC의 연동은 아직 없으며 기록은 자동 삭제되지 않는다. 외부 의존성 추가 없음.
