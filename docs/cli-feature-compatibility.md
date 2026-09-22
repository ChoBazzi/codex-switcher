# Codex 부가 기능의 프록시 호환성 조사

**여러 메인 대화:** [ADR 0070](adr/0070-multiple-cli-connections.md)에 따라 앱에서 최대 5개 독립 연결을 만들고 각 연결의 명령으로 별도 대화를 실행할 수 있다. 같은 연결에서 다른 루트의 `/new`·`/fork`를 실행하는 제한은 그대로이며, 계정·사용량은 공유한다.

**인라인 이미지 도구 결과:** [ADR 0063](adr/0063-inline-tool-image-history.md)에서 function/custom 도구 결과의 텍스트와 base64 data URL 이미지를 보존하도록 추가했다. 외부 이미지 URL·file ID·일반 메시지 이미지·서버 실행 도구는 여전히 지원 범위 밖이며 전체 요청 4 MiB 제한을 유지한다.

**후속 수정:** [ADR 0058](adr/0058-auxiliary-conversation-isolation.md)에서 리뷰·서브에이전트·자동 승인 지원을 추가했다. 아래 표는 수정 전 조사 기록이다. 현재 같은 이름의 통합 검사는 이 세 보조 경로의 통과와 기존 루트 분기 제한을 확인한다.

**2026-09-21 호환성 수정:** CLI 0.155.1의 `agent_message` 입력 이력이 `probe_tool_history_unsupported`로 차단되는 경로를 합성 재개로 재현하고 텍스트 형식 지원을 추가했다. item ID는 제거하고 출처·텍스트를 보존하며 실제 사용자 입력 경계와 계정 격리는 유지한다. [ADR 0043](adr/0043-local-tool-history-switching.md) 및 `TestInstalledProbeToolsAgentMessageHistory` 참조. 로컬 기록의 내부 메타데이터는 CLI가 wire에서 생략하므로 임의 메타데이터 통과는 허용하지 않는다.

검증일: 2026-09-14. 설치 CLI: `codex-cli 0.154.0`, macOS arm64.

## 확인 결과

실계정 없이 **설치 CLI → 실제 switch-probe 구현 → 합성 upstream**으로 검사했다. 실패를 예상하는 특성 검사이므로 테스트 PASS는 해당 기능 지원이 아니라 현재 차단 현상이 재현됐다는 뜻이다. 제품의 수락 정책은 변경하지 않았다.

| 기능 / 조건 | 관찰 결과 | 원인 |
| --- | --- | --- |
| `codex review --uncommitted` | `409 probe_identity_invalid` | Thread-Id와 Session-Id가 각각 하나지만 서로 다름 |
| 서브에이전트 `spawn_agent`, 새 문맥 | 동일 오류 | 자식 요청의 두 ID가 다름 |
| 서브에이전트 `spawn_agent`, `fork_context=true` | 동일 오류 | 문맥 복사 여부와 무관하게 자식 요청의 두 ID가 다름 |
| `--approve-for-me`에서 승격 요청 자동 검토 | 동일 오류 | 승인 검토용 요청의 두 ID가 다름 |
| 기존 연결에서 `codex exec fork` | `409 probe_conversation_changed` | 두 ID는 같지만 프록시에 고정된 기존 대화 ID와 다름 |
| 기존 연결에서 새로운 `codex exec` 실행 | 동일 오류 | 새 대화 ID가 기존 연결과 충돌 |
| 같은 대화 `codex exec resume` | 성공 | 원래 대화 ID 유지 |
| 일반 로컬 도구 왕복 | 기존 합성 검사 성공 | 같은 대화 안의 도구 이력 처리 |
| CLI 텍스트 자동 압축 → 후속 요청 → 계정 전환 | 기존 합성 검사 성공 | 같은 대화와 텍스트 요약 보존 |

추가 로컬 헤더 관측에서 서브에이전트·자동 승인 요청은 **Session-Id가 부모 Thread-Id와 같고, 자식 Thread-Id는 별도 값**이었다. 정상 요청과 같은 대화 재개는 두 헤더가 모두 원래 대화 ID였다. 따라서 Session-Id를 단순히 OS 프로세스별 ID라고 단정할 수 없다. CLI 소스의 전체 의미 계약과 모든 앱 전송 경로를 검증한 것은 아니다.

리뷰는 upstream 호출 0회, 분기/새 대화는 최초 정상 요청 1회 이후 추가 upstream 호출 없이 차단되는 것도 검사했다. 서브에이전트·자동 승인에서는 부모 요청이 계속 완료될 수 있으므로 **CLI 전체 종료 코드 0만으로 자식 작업 성공을 판단하면 안 된다**. 검사에서는 실제 프록시의 차단 이벤트를 확인한다.

## 비슷한 오류가 가능한 경로와 한계

- `/review`, `/fork`, `/new`, 다른 기록 선택: 대응하는 비대화형 CLI 경로를 검사했다. TUI 버튼·슬래시 명령 자체를 자동화한 것은 아니다. 기존 프록시에 새 ID를 보내는 경우 같은 제한을 받는다.
- 여러 터미널/IDE 작업이 하나의 앱 연결을 공유: 다른 대화 ID이면 `probe_conversation_changed`, 이미 HTTP 요청이 처리 중이면 검사 순서상 `probe_request_in_progress`가 먼저 나올 수 있다. 별도 UI 클라이언트를 직접 실행한 결과가 아닌 현재 admission 코드에 근거한다.
- 서브에이전트 후속 작업·재개: 새 자식의 최초 요청부터 차단되므로 send/followup/resume까지 성공하는 계약은 아직 검증할 수 없다. 모든 서브에이전트 명령이 그 자체로 모델 요청을 만드는 것은 아니다.
- 메모리 생성 등 백그라운드 모델 작업: 별도 대화 ID나 누락된 헤더를 사용하면 같은 제한 대상이지만 해당 기능은 직접 재현하지 않았다. 실패 확정 목록에 포함하지 않는다.
- 파일 편집·일반 셸 도구·로컬 MCP 실행은 별도 모델 대화를 만들지 않는 한 이 오류의 직접 원인이 아니다. 도구가 추가 Codex를 실행하거나 자동 승인을 요청하면 해당 경로의 제한이 적용된다.
- native 암호화 압축은 ADR 0049의 계정 소유권 제한을 별도로 따른다. 이번 텍스트 압축 성공을 이미지·웹 검색·모든 MCP·native 압축 지원으로 확대 해석하지 않는다.

## 코드 근거와 수정 방향

`internal/cliidentity/identity.go`의 `ThreadID`는 헤더 개수·동일성·UUID를 검사한다. `cmd/switcher-helper/switchprobe.go`의 `probeAdmissionCode`는 식별 오류 → 요청 처리 중 → 이전 실패 → 다른 대화 순서로 거절한다. daemon은 `probe_blocked` 진단을 보관하거나 앱에 전달하지 않으므로 과거 사용자 요청의 헤더 상태는 소급 확인하지 못한다.

두 헤더의 동일성 검사만 제거해도 충분하지 않다. 기존 `session`, `busy`, `failed`, handler/도구 이력, 암호화 소유권, checkpoint가 단일 대화 기준이다. 지원 구현 시 부모-자식 관계 검증, 대화별 상태 격리, 동시 요청 정책 및 계정 고정 범위를 함께 설계해야 한다. 헤더는 인증 수단이 아니므로 Session-Id 일치만으로 새 대화를 신뢰해서는 안 된다. 실패 요청 자동 재전송 금지는 유지해야 한다. 이 문서는 조사 결과이며 다중 대화 지원 설계를 확정하는 ADR이 아니다.

## 재현

새 검사 `TestInstalledProbeAuxiliaryIdentity`는 review/spawn/spawn_fork/guardian/resume/fork/new의 7개 경우를 실행한다. 별도 임시 HOME/CODEX_HOME·합성 저장소와 고정 SSE 도구 응답을 사용한다. 실제 설정·계정·요청 본문·식별자는 저장소나 검사 출력에 기록하지 않는다. 추가 의존성은 없다. 현재 설치 CLI의 `multi_agent_v1` 도구 계약을 사용한다.

```sh
SWITCHER_CODEX_INTEGRATION=1 \
GOCACHE=/private/tmp/codex-switcher-go-build \
GOMODCACHE=/private/tmp/codex-switcher-go-mod \
go test -race -count=1 -timeout 110s -v ./cmd/switcher-helper \
  -run '^TestInstalledProbeAuxiliaryIdentity$|^TestInstalledProbeCompaction$|^TestInstalledProbeTools$'
```

상기 합성 검사 통과. 실제 OpenAI 모델 호출, 실행 중인 사용자 daemon 변경 및 코드 수정으로 호환성 제한 해제는 수행하지 않았다.
