# ADR 0043: 로컬 도구 이력과 턴 경계 전환

- 상태: accepted (실험 기능·합성 검증, 실계정 개발 작업 검증 대기)
- 관련: ADR 0040, 0042

## 결정

후속 [ADR 0049](0049-compaction-and-account-ownership.md)에서 CLI 텍스트 압축 후 전환을 검증하고 native 암호화 압축은 생성 계정에서만 처리하도록 추가했다. 아래 압축 미지원 설명은 당시 구현 범위다.

앱이 실행하는 switch-probe에 `--tools`를 추가한다. 기존 플래그 없는 텍스트 실험은 보존한다. 프록시는 도구를 직접 실행하지 않는다. Codex CLI의 function/custom 도구 선언과 호출·결과를 전달한다. 웹 검색 등 서버 실행 도구, 이미지·파일 참조, item_reference, previous_response_id, conversation, compaction은 지원하지 않는다. 따라서 일반 Codex의 모든 기능을 지원하는 단계는 아니다.

- 완전한 function_call/function_call_output, custom_tool_call/custom_tool_call_output 쌍만 허용한다. orphan·중복·결과 누락·타입 불일치는 고정 오류로 차단한다. 도구 결과의 텍스트·호출 인자·assistant phase는 보존한다.
- 마지막 사용자 메시지 이전의 완료된 도구 이력은 서버 item id를 제거하고 call_id를 실행별 비밀값·대상 슬롯 기반의 안정적인 로컬 ID로 치환한다. 호출과 결과를 동일하게 치환하며 A 식별자를 그대로 B에 보내지 않는다.
- 이전 턴 reasoning은 전송에서 제외한다(로컬 CLI 기록은 변경하지 않는다). 현재 턴 reasoning과 call_id는 직전 요청을 처리한 동일 계정에만 그대로 반환한다. 암호화된 추론 상태를 다른 계정으로 전송하거나 복호화하지 않는다. 이전 추론이 모두 유지되는 투명 프록시는 아니므로 품질·캐시 영향은 실사용 검증 대상이다.
- SSE의 도구 출력은 HTTP 완료 이후에도 턴 미완료로 취급한다. 도구 왕복 중에는 수동 선택을 거절하고 자동 계정 재선택을 하지 않는다. assistant 최종 응답과 정상 SSE 완료 이후 다음 요청에서 5% 정책을 적용한다. 턴 도중 실제 한도가 소진되면 실패로 차단하며 재전송하지 않는다.
- 완료 판별이 불가능한 응답·불완전 스트림·미지원 출력은 실패 처리한다. 응답 내용을 로그/DB에 저장하지 않고 메모리에서 1 MiB 제한으로 이벤트를 관찰한다.
- 샌드박스 권한은 자동 확장하지 않는다. 기본 read-only이며 파일 수정을 시험할 때 사용자가 격리된 테스트 디렉터리에서 workspace-write를 명시한다. UI 레이아웃은 변경하지 않고 기존 기능 안내 문구만 맞춘다.

## 근거와 한계

후속 [ADR 0048](0048-explicit-tool-turn-abandonment.md)에서 HTTP 완료 후 도구 대기 잠금을 사용자가 명시적으로 해제하는 복구 경로를 추가했다. 자동 취소 감지나 로컬 도구 종료 기능은 아니다.

[공식 function calling](https://developers.openai.com/api/docs/guides/function-calling)은 call_id로 호출과 결과를 연결하며 custom 도구도 별도 호출/결과 항목으로 다룬다. [공식 reasoning 문서](https://developers.openai.com/api/docs/guides/reasoning)는 마지막 사용자 메시지 이후 도구 왕복에 필요한 reasoning과 호출·결과를 유지하도록 안내한다. 이 자료는 서로 다른 ChatGPT 계정 간 암호화 상태 호환성을 보장하지 않는다. 그래서 현재 턴을 고정하고 이전 턴의 불투명 상태를 내보내지 않는 로컬 정책을 적용했다.

압축 지원, 서버 실행 도구, 이미지, 다중 CLI, 대화 복원·재시작은 별도 작업이다. 도구 실행을 사용자가 중단해 최종 응답이 오지 않으면 안전상 턴 잠금이 남을 수 있다. 취소 감지/명시적 잠금 해제 UX는 후속 작업이며 미완료 도구를 재실행해 복구하지 않는다. 앱·프록시 사용량 중복 조회도 이 변경에서 통합하지 않는다.

## 에이전트 간 텍스트 이력 호환성 (2026-09-21)

설치 CLI 0.155.1에서 합성 로컬 이력을 재개하면 `agent_message` 항목이 Responses 입력에 그대로 포함된다. 기존 도구 이력 파서의 기본 거절 분기로 인해 `history_type_unsupported` 및 HTTP `409 probe_tool_history_unsupported`가 재현됐다. 같은 합성 기록의 `internal_chat_message_metadata_passthrough`는 CLI가 전송 전에 제거하므로 이 메타데이터를 모든 항목에 허용하는 변경은 하지 않는다. 실제 실패 요청 본문과 상세 진단은 보관하지 않았으므로 과거 요청 전체를 재구성한 결과는 아니다.

tools 경로는 `agent_message`의 `type`, 문자열 `author`/`recipient`, `input_text`/문자열 `text`로 구성된 content 배열만 허용하고 item `id`는 제거한다. 출처와 텍스트를 보존하되 author/recipient로 계정·대화 라우팅이나 인증을 결정하지 않는다. 일반 user 메시지로 변환하거나 새 사용자 입력·턴 경계로 취급하지 않는다. reasoning/compaction 소유권 및 도구 호출/결과 쌍 검증은 그대로 유지한다. 이미지·파일·참조·암호화·알 수 없는 추가 필드는 거절한다.

이는 CLI가 전달하는 입력 이력 호환성 수정이며, 새로운 서버 실행 도구나 SSE 출력 타입 지원이 아니다. 사용자 기록을 수정하거나 실패 요청을 자동으로 재전송하지 않는다. 적용은 CLI가 입력 대기 중일 때 새 helper 빌드 후 앱·프록시를 종료하고 재실행한다. 실패 상태가 남으면 기존 복구 준비를 누르고 CLI에 **새 지시**를 입력한다. 같은 기록을 유지하려면 `proxy-stop --new-session`을 사용하지 않는다.

## 검증

- TestProbeTools*: 호출 쌍·텍스트 보존, 계정별 ID 격리, 현재 턴 reasoning 유지/타계정 거절, 참조·압축·잘못된 이력 차단.
- TestProbeTurnBoundary: 분할/CRLF SSE 원문 보존, function/custom 도구와 최종 답변 구별, 미지원 출력·버퍼 제한.
- TestInstalledProbeTools: 실제 CLI → 실제 managed probe → 합성 서버. 로컬 도구 런타임 결과, 현재 턴 A 유지, 정상 완료 후 B 선택, 같은 thread 재개와 과거 도구 결과 전달, 정확히 3회 요청을 확인한다. 실제 모델/계정 호출은 없으며 동일 TUI 수동 테스트는 별도다.
- TestProbeToolsAgentMessage*: 에이전트 텍스트·출처 보존과 ID 제거, 미지원 필드/콘텐츠 거절, 사용자 경계 불변, 현재 턴의 타 계정 reasoning/도구 전달 차단.
- TestInstalledProbeToolsAgentMessageHistory: 설치 CLI 0.155.1의 합성 기록 재개 → 실제 입력 파서 → 합성 서버. agent_message 전송과 metadata 생략, 수정 후 재개 성공 및 B 정규화 시 텍스트·출처 보존을 확인한다. 실계정 upstream 수락이나 모든 에이전트 메시지 형식의 호환성을 보장하는 검사는 아니다.
- 테스트 호스트에서 sandbox-exec가 거절되어 터미널/파일 변경 성공을 주장하지 않는다. 설치 CLI의 custom exec 런타임이 고정 문자열을 반환하도록 검사한다. 로컬 파일 수정 절차는 README에 명시한다.
