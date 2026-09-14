# ADR 0039: 텍스트 전용 실계정 전환 실험

- 상태: experimental, 텍스트 전용 실계정 수동 전환 사용자 확인
- 날짜: 2026-09-08

2026-09-09 사용자가 동일 CLI에서 A→B 전환 후 정상 동작을 보고했다. 일반 도구·압축·다중 세션 검증을 의미하지 않는다. 제품 설계 반영은 [ADR 0040](0040-same-conversation-switch-and-optional-wiki.md)을 따른다.

## 기본 모델 요청 호환성 수정

assistant 메시지의 `phase`는 서버 참조가 아니라 commentary/final_answer 구분 메타데이터다. [OpenAI phase 안내](https://developers.openai.com/api/docs/guides/latest-model?model=gpt-5.5)에 따라 두 값을 검증하고 그대로 보존한다. 다른 역할·알 수 없는 값은 차단한다. 합성 응답에 final_answer를 포함해 설치된 CLI 재개 시 실제 B 요청까지 보존되는지 회귀 검사한다. 기존 fixture에는 phase가 없어 이 오류를 놓쳤다.

설치된 Codex 0.153.4 + 임시 CODEX_HOME + 합성 upstream으로 첫 input의 `type=additional_tools`를 재현했다. 합성 모델명 switcher-synthetic에서는 나타나지 않아 기존 테스트가 놓쳤다. 이는 서버 continuation이 아니라 인라인 도구 선언이다. 텍스트 전용 실험에서는 알려진 선언 외곽 필드와 tools 배열을 검사한 뒤 해당 항목을 제외한다(이미 top-level tools도 비활성화한다). 메시지 문맥은 보존하며 알 수 없는 항목은 계속 차단한다.

회귀 검증은 설치된 CLI 기본 모델 설정으로 첫 요청 → 같은 대화 exec resume을 실제 검사 함수와 프록시를 통해 합성 upstream에 전송한다. 합성 A/B 인증 변경, 재개 문맥, 인라인 선언 제외, 정확히 두 번 전송을 확인한다. 이 검증은 실계정·동일 TUI 검증을 대신하지 않는다.

본문 거절 진단에는 `detail.reason`과 0부터 시작하는 item/part 인덱스를 추가한다(해당 없음은 -1). JSON 형식, 서버 참조, 항목 유형, 역할, 추가 메시지 필드, 콘텐츠 유형을 구분한다. 이름은 고정 허용 목록만 출력하고 미지의 필드/유형은 unknown으로 분류한다. 원문·인증·실제 식별자는 출력하지 않으며 수락 정책과 재시도 금지는 유지한다.

첫 요청 409 진단: 공통 `probe_session_blocked`를 identity_invalid/request_in_progress/previous_request_failed/conversation_changed로 분리한다(각각 `probe_` 접두사). 서버에는 헤더 개수·두 값의 동일 여부만 기록하고 실제 ID는 기록하지 않는다. 수락 조건은 완화하지 않는다.

`switch-probe --allow-live`는 기존 제품 경로와 별도의 임시 CODEX_HOME/loopback 서버를 만든다. 사용자가 stock `codex`를 직접 실행하며 helper는 CLI를 호출하지 않는다. 서버 stdin의 a/b 선택은 요청 처리 중에는 거절하고 이후 요청에만 적용한다. 첫 요청의 대화 ID 하나에 한정하며 실패 후 서버 수명 동안 재요청을 거절한다. 시작·선택만으로 모델 호출하지 않는다.

기존 DB·훅·Wiki·세션 고정 정책은 변경하지 않는다. 인증만 교체하는 무수정 투명 프록시 검증이 아니라 **텍스트 이력 정규화를 포함한 제한적 실험**이다. 모든 요청에서 message id를 제거하고 도구를 비활성화한다. 이전 응답/대화 참조, reasoning, 함수·도구 결과, 이미지와 미지원 이력은 전송 전 차단한다. 원본 텍스트는 보존하며 서버 continuation ID를 다른 계정에 전달하지 않는다. reasoning이 포함된 일반 대화는 차단될 수 있으므로 성공을 보장하지 않는다.

실계정 토큰은 기존 Keychain에서 요청 시 읽고 CLI에는 임의 로컬 비밀값만 제공한다. 로그에는 슬롯·고정 오류 코드·진단 카운터만 출력한다. 서버 종료 시 변경되지 않은 임시 config만 제거하고 CLI 대화 기록은 남긴다. 출력된 임시 경로를 사용자가 관리해야 한다. 프로덕션 자동전환이나 앱 연동으로 해석하지 않는다.

검증: 텍스트 보존/message id 제거/불투명 참조 차단 단위 테스트. 실제 A 응답 완료 → stdin b 선택 → 같은 TUI에서 기억 질문은 사용자가 별도로 실행한다. 실패 요청은 다시 보내지 않는다. 실험 성공만으로 도구 작업·압축·장시간 세션 호환성을 주장하지 않는다.
