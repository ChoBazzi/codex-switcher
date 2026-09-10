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

## 검증

- TestProbeTools*: 호출 쌍·텍스트 보존, 계정별 ID 격리, 현재 턴 reasoning 유지/타계정 거절, 참조·압축·잘못된 이력 차단.
- TestProbeTurnBoundary: 분할/CRLF SSE 원문 보존, function/custom 도구와 최종 답변 구별, 미지원 출력·버퍼 제한.
- TestInstalledProbeTools: 실제 CLI → 실제 managed probe → 합성 서버. 로컬 도구 런타임 결과, 현재 턴 A 유지, 정상 완료 후 B 선택, 같은 thread 재개와 과거 도구 결과 전달, 정확히 3회 요청을 확인한다. 실제 모델/계정 호출은 없으며 동일 TUI 수동 테스트는 별도다.
- 테스트 호스트에서 sandbox-exec가 거절되어 터미널/파일 변경 성공을 주장하지 않는다. 설치 CLI의 custom exec 런타임이 고정 문자열을 반환하도록 검사한다. 로컬 파일 수정 절차는 README에 명시한다.
