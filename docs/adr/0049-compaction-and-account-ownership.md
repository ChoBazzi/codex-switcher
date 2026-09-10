# ADR 0049: CLI 텍스트 압축과 암호화 압축 소유권

- 상태: accepted (설치 CLI 합성 검증; 실계정 검증 대기)
- 날짜: 2026-09-10
- 관련: ADR 0043, 0045, 0048

## 확인한 동작

설치된 Codex CLI 0.153.4는 현재 `switch_probe` 커스텀 provider에서 일반 `/responses`로 텍스트 요약을 만든다. 합성 usage token 수와 테스트 실행에만 적용한 model_auto_compact_token_limit으로 자동 압축을 유발했다. CLI의 로컬 `compacted` 기록, 이후 요청에 요약 포함, 같은 thread를 B로 전환한 뒤에도 요약 포함을 확인했다. 따라서 이전 문서의 '모든 compaction 미지원' 표현은 이 경로에는 맞지 않는다. 테스트용 임계값을 실제 앱 프로필에 적용하지 않는다.

텍스트 압축은 CLI가 담당한다. 프록시는 요약 프롬프트를 삽입하거나 Wiki를 만들지 않고 기존 텍스트/도구 이력 검사를 사용한다. B 요청에는 압축된 텍스트가 전달되며 이전 턴의 opaque reasoning과 서버 참조를 내보내지 않는 정책도 유지한다. 실제 모델의 요약 품질 및 TUI 수동 /compact는 별도 사용자 검증이다.

## Native 압축

공식 standalone 압축은 `POST /responses/compact`에 대응하는 JSON window를 반환하며 encrypted_content 항목이 포함된다. 반환 window를 임의로 줄이거나 암호화 항목을 복호화하지 않는다.

- tools 모드의 managed probe에서 standalone 경로를 활성화한다. 일반 legacy/persistent 프록시에 자동으로 열지 않는다.
- 기존 인증·동일 대화 식별·요청 직렬화·10분 상한·단일 upstream 시도를 유지한다. 도구 결과 대기 중 native 압축은 거절한다.
- upstream 대상은 기존 responses endpoint의 `/compact` 하위 경로다. 클라이언트 Authorization/Cookie를 전달하지 않는다.
- 응답 전체를 최대 4 MiB로 제한하고 정상 EOF, JSON window, 지원 항목, compaction shape를 검사한 뒤 성공 응답을 내보낸다. 실패·리다이렉트·잘못된 응답에는 고정 코드만 반환하고 자동 재시도하지 않는다.
- opaque 항목은 메모리에 최대 1,024개 해시와 생성 슬롯/인증 토큰 해시만 등록한다. 원문·계정 ID·토큰을 로그나 DB에 기록하지 않는다. 입력의 opaque 항목은 생성 슬롯과 인증이 일치해야 한다. 실제 dispatch 인증도 다시 검증한다.
- native 압축 window가 활성인 동안 다른 슬롯 선택과 그 슬롯으로의 자동 전환을 거절한다. 새 텍스트 window가 들어와 opaque 의존성이 없어지면 해제한다. 프록시가 원본 과거 대화를 복원하거나 추가 요약 호출을 만들지 않는다.
- 인증 재등록/변경 또는 프록시 재시작으로 소유권을 확인할 수 없으면 opaque 항목을 거절한다. 같은 슬롯이라는 이유만으로 허용하지 않는다.

**A의 암호화 압축 결과를 B가 수락하는지는 검증되지 않았다.** 계정 간 사용을 허용했다는 뜻이 아니다. 현재 계정 전환 지원 경로는 CLI 텍스트 압축이다. 서버 내 자동 compaction, 이미지·파일·서버 참조와 미완료 도구 이력은 이번 지원 범위가 아니다.

## 검증

- TestInstalledProbeCompaction: 실제 설치 CLI → 실제 managed probe → 합성 서버. 첫 답변/A 압축/A 후속/B 후속 총 4회 호출, 같은 thread와 요약 보존, 로컬 compacted 경계 확인.
- TestProbeNativeCompaction: native JSON 전달, A 재사용, B 선택 차단, 새 텍스트 window 이후 전환, B에 과거 opaque 재주입 무호출 거절.
- TestProbeCompactOwnership: 원본 ID 보존, 다른 슬롯/변경 인증 거절, 불완전 응답의 부분 등록 방지.
- TestCompactEndpointSingleAttempt: 경로·인증 격리, 정상/누락 media type, 오류/oversize/잘못된 JSON/redirect, 실패 재시도 금지.

합성 검사는 OpenAI 실계정의 암호화 호환성이나 실제 모델 요약 품질을 증명하지 않는다.

## 공식 근거

- https://developers.openai.com/api/docs/guides/compaction
- https://developers.openai.com/api/reference/java/resources/responses/methods/compact
