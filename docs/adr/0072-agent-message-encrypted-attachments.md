# ADR 0072: 인증 소유권을 검증한 에이전트 작업 지시 보존

- 상태: accepted
- 날짜: 2026-09-23
- 관련: ADR 0043, 0058, 0061, 0064, 0071

## 문제와 근거

협업 작업의 첫 요청에 `agent_message.content`의 `input_text`와 `encrypted_content`가 함께 들어왔다. 텍스트만 허용한 기존 파서는 이를 거절했다. 암호화 첨부를 제거하는 초기 수정은 에이전트가 시작돼도 작업 지시가 비었다고 답하는 결함을 만들었다. 일반 텍스트가 존재한다는 것만으로 실제 지시가 보존된다고 볼 수 없다. 텍스트가 단순한 TASK/Payload 안내이고 실제 지시가 암호화 부분에 들어있는 경우를 합성 테스트에 포함해야 한다.

실제 보조 대화의 로컬 기록은 원문·식별자를 출력하지 않고 구조와 값의 동일성만 확인했다. 확인한 10개 암호화 content는 부모/자식이 실행한 협업 도구 호출의 `message` 인자와 정확히 일치했다. namespace는 `collaboration`이었다. 암호문을 해독하거나 실제 요청/응답을 fixture로 저장하지 않았다. 이 관측은 CLI 전달 경로의 근거이며 모든 서버 버전과 기능을 보장하지 않는다.

## 결정

- 암호화 지시를 삭제하거나 평문으로 바꾸지 않는다. 알려진 출처와 같은 인증 신원임을 검증한 경우에만 content를 그대로 전달한다. 모르는 암호문을 현재 슬롯의 소유로 추정하지 않는다.
- 하나의 연결의 루트와 보조 대화가 공유하는 소유권 레지스트리를 둔다. 다른 연결은 공유하지 않는다. 키는 메시지 SHA-256과 실제 응답을 생성한 `HistoryCredential` 해시의 쌍이다. 로그인 등록 세대·계정·사용자 격리는 ADR 0061/0064를 따른다.
- 성공한 upstream SSE 응답에서 관측한 `function_call`만 등록한다. namespace는 `collaboration`/`multi_agent_v1`, name은 `spawn_agent`/`send_message`/`followup_task`를 허용한다. 메시지는 JSON arguments의 문자열 `message`다. 요청의 author/recipient, 클라이언트가 제출한 과거 도구 호출 또는 슬롯 이름만으로 등록하지 않는다. 추가 도구 이름/형식은 관측과 검증을 거쳐 명시적으로 확장한다.
- 응답 완료와 이력 검증을 통과한 뒤 소유권을 등록하고, CLI에 terminal 이벤트를 내보내기 전에 checkpoint에 저장한다. 실패·취소·불완전 응답에서는 등록하지 않는다. 한 연결에 최대 1,024개 (메시지, 인증) 쌍을 보관하며 자동 eviction이나 소유권 재할당을 하지 않는다.
- 기존 비공개 checkpoint에 메시지·인증 해시만 추가한다. 본문·암호문·OAuth·실계정 ID는 저장하지 않는다. 복원 시 개수·영값·중복을 검증하고 안정적인 정렬로 불필요한 쓰기를 피한다. 읽기 한도 안에서 기존 압축 레지스트리와 함께 최대 용량을 검증한다. 필드가 없는 기존 checkpoint는 빈 소유권으로 읽는다. 과거 요청 이력으로 소유권을 추정해 채우지 않는다. 구버전 helper로의 다운그레이드는 새 필드를 지원하지 않는다.
- 입력의 `agent_message`는 type/id/author/recipient/content 계약을 유지한다. content는 기존 input_text와 정확한 두 필드(type/encrypted_content)의 비어 있지 않은 문자열 암호문만 허용한다. 미지원 필드·참조·잘못된 값은 거절한다. 검증된 암호문만 있는 메시지도 보존한다. item ID만 제거하며 텍스트·암호문·순서를 바꾸지 않는다. 사용자 턴 경계나 인증·라우팅 근거로 취급하지 않는다.
- 암호문이 현재 계정 인증에서 관측됐는지 본문 정규화 때 검사하고 실제 dispatch에서도 같은 인증인지 재검사한다. 새 보조 대화도 검증한 인증에 고정한다. 본문 검사 후 재로그인·인증 교체로 다른 신원에 전달되는 것을 막는다.
- 같은 암호문이 원래 협업 도구 호출의 arguments에도 남는다. 해당 message 인자도 동일 소유권 검사와 dispatch 고정을 적용해 과거 call_id 치환만으로 다른 계정에 수출되지 않게 한다. 도구 메시지가 평문인지 암호문인지 임의 추정하지 않고 알려진 협업 호출의 모든 message 인자에 보수적으로 적용한다.
- 명시적 한도 후 portable 정규화는 암호화 agent_message와 보호된 협업 호출을 거절한다. reasoning 제거처럼 삭제하고 자동 전환하지 않는다. 암호화 협업 이력이 있는 요청의 계정 간 자동 전환은 제한된다. 기존 reasoning/compaction 및 도구 쌍 검증을 완화하지 않는다.
- 보조 실패 잠금은 유지한다. 재시작에서 복원된 보조 작업은 `probe_auxiliary_restart_required`로 일반 `probe_previous_request_failed`와 구분한다. UI에 새 명시적 작업 또는 검토 대화가 재사용될 때 새 연결이 필요함을 안내한다. 소유권 복원은 실패 작업의 자동 재개 승인이 아니다.
- 소유권 미확인은 `agent_message_owner_unavailable`로 분리한다. 진단에는 메시지/암호문/해시/경로를 넣지 않는다. 추가 의존성 없음.

## 한계 및 적용

새 helper 적용 전 생성된 암호화 협업 이력은 출처 해시가 없으므로 승인할 수 없다. 같은 이력을 유지한 대화는 새 사용자 입력만으로 해결되지 않을 수 있으며 새 연결의 새 대화가 필요하다. 새 버전에서 생성하고 checkpoint에 기록한 소유권은 재시작 뒤에도 동일 인증에서 검증된다. 실패한 보조 대화 자체는 재개하지 않는다.

실제 계정의 암호화 지시 수락과 의미적 실행은 새 빌드 적용 후 별도 검증해야 한다. 단순 HTTP 성공이나 에이전트 시작 이벤트만 성공으로 판정하지 않고, 전달한 구체적 작업 결과를 확인한다. 기존 앱·daemon은 작업 중 교체하지 않는다.

## 검증

- `TestProbeToolsAgentMessageEncryptedAttachment`: 알려진 소유권의 지시를 바이트 값 그대로 보존하고 item ID만 제거한다. TASK/Payload 안내문과 암호문만 있는 입력, 사용자 경계 불변, 미확인·portable 거절과 별도 reasoning/도구 쌍 보호를 검사한다.
- `TestProbeToolsAgentMessageRejectsUnsafeShape`: 잘못된 암호문·참조·알 수 없는 필드 거절.
- `TestProbeAgentOwnershipObservation`, `TestProbeAgentOwnershipCapacity`: 성공 응답의 협업 message 관측, 인증·연결 격리, 도구 호출 인자 경유 유출 방지, 허용 도구 범위와 유한 레지스트리.
- `TestProbeAgentOwnershipCheckpoint`: 해시 복원과 변경 인증 거절, 원문 미저장, 손상·중복·과대 레지스트리 거절, 압축 레지스트리와 함께 최대 용량 읽기.
- `TestProbeAgentTaskDispatch`: 실제 루트 응답으로 등록한 지시의 자식 전달, 재시작 복원, 인증·등록 세대 및 dispatch 직전 교체, 미확인 지시 차단과 실패 무재전송.
- `TestProbeAgentDispatchGuard`: 포트 없이 실제 보조 handler/resolver를 실행해 미확인 지시, 변경 인증·등록 세대, dispatch 직전 인증 교체와 취소의 upstream 시도 0회를 검사한다.
- `TestAuxiliaryAgentEncryptedAttachment`: 보조 handler가 지시를 삭제하지 않고 전달하는지 검사한다.
- `TestInstalledProbeToolsAgentMessageHistory/agent_encrypted`: 합성 로컬 이력의 설치 CLI 직렬화에서 암호화 content 보존과 다른 소유권 거절. 실제 레지스트리 등록은 위 dispatch 검사에서 확인한다.
- `TestProbeAgentRestoredFailureDiagnostic`, 진단 redaction 검사: 재시작 보조 잠금과 현재 실패를 구분하고 추가 원문을 노출하지 않는다.

2026-09-23: 포트 없는 소유권·checkpoint·보조 dispatch 차단·파서·기존 도구 이력·진단 검사를 `go test -race`로 통과했다. 전체 패키지 및 테스트 컴파일, `go vet ./...`, helper·Xcode Swift 빌드, 실행 파일 서명 검사, `DirectProxyCheck`도 통과했다. 실제 loopback 기반 검증 명령은 자동 승인 검토의 `409 probe_previous_request_failed`로 실행 전 차단됐다. 신규 loopback/설치 CLI 검사, 전체 검증 및 실계정 수락은 아직 완료하지 못했다. 실행 중인 사용자 앱·daemon은 변경하지 않았다.

2026-09-23 후속 검증: 쓰기 권한이 허용된 세션에서 loopback 바인딩 제한을 확인한 뒤 권한 확대 승인을 받아 아래 합성 통합 검사를 모두 통과했다. 설치 CLI는 `0.155.1`이다. 이전의 `409 probe_previous_request_failed` 승인 검토 오류는 재발하지 않았다.

```sh
SWITCHER_CODEX_INTEGRATION=1 \
GOCACHE=/private/tmp/codex-switcher-go-build \
GOMODCACHE=/private/tmp/codex-switcher-go-mod \
go test -race ./cmd/switcher-helper \
  -run '^(TestProbeAgentTaskDispatch|TestAuxiliaryAgentEncryptedAttachment|TestInstalledProbeAuxiliaryIdentity|TestInstalledProbeToolsAgentMessageHistory)$' \
  -count=1 -timeout 120s
```

이어서 `DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer sh macos/Checks/verify.sh` 전체를 통과했다. 이번 소유권 구현을 포함한 Go race/vet, 설치 CLI 합성 통합 검사, helper·Swift 빌드, 모델·제어 서버·로그인·DirectProxy 검사 및 밝은/어두운 UI 검사를 완료했다. 코드 수정이 필요한 검사 실패는 없었으며 기존 Swift 캡처 및 deprecated API 경고는 남아 있다. ADR 0071 검증 결과를 대신 적용한 것이 아니라 이번 변경 상태에서 실행한 결과다. 실행 중인 사용자 앱·daemon은 종료하거나 교체하지 않았다.

## 남은 실계정 검증

계정 활동 잠금을 읽기 전용으로 확인한 결과 다른 실행이 잠금을 보유하고 있었다. 별도 최신 helper의 managed 도구 경로도 같은 활동 잠금을 요구하므로 기존 연결을 유지하는 이번 작업에서는 실계정 모델 요청을 시작하지 않았다. 잠금 파일·기존 JSONL·checkpoint·인증 정보는 변경하지 않았다. 기존 `live-test`는 도구 없는 인사 요청 전용이므로 협업 지시 검증을 대체하지 않는다.

실계정 검증은 모든 연결의 모델·도구 작업이 끝나 활동 잠금이 해제된 뒤, 검증된 helper의 별도 임시 연결과 새 CLI 대화에서 수행해야 한다. 기존 대화를 resume하거나 과거 소유권을 채우지 않는다. 새 부모 응답에서 생성한 협업 지시를 자식에게 전달하고, 지시에만 포함된 고정 계산이나 문자열 변환의 예상 결과와 자식의 실제 결과를 비교한다. HTTP 성공·에이전트 시작·부모 종료 코드만으로 성공 판정하지 않는다. 기존 인증을 변경해야 진행 가능한 경우에는 이 검증 범위에서 갱신·재로그인을 실행하지 않는다. 출력과 기록에는 성공 여부·고정 진단만 남기며 원문·암호문·실제 식별자는 포함하지 않는다.

ADR 0071의 설치 CLI 경유 자동 전환과 실제 계정 간 수락 역시 별도 미검증이다. 이번 설치 CLI 검사는 암호화 이력 직렬화·보조 대화 경로이며 사용량 소진 전환의 실계정 검증을 대신하지 않는다.
