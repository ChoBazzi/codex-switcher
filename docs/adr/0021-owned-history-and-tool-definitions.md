# ADR 0021: 소유 대화 이력과 인라인 도구 정의

- 상태: accepted
- 날짜: 2026-09-06
- 관련: ADR 0019, 0020

## 배경과 결정

다음 CLI 요청은 이전 assistant/reasoning 항목을 전체 내용으로 다시 전달할 수 있다. ID만 검사하는 방식은 수정된 이력을 구별하지 못하므로, 완료 응답에서 관찰한 내용의 정규화 SHA-256을 소유권 키로 저장한다. 내용과 동일성 확인에 필요한 해시만 저장하며 원문·암호화된 reasoning 본문은 저장하지 않는다.

- 최종 완료 응답에서 assistant message 및 reasoning 항목의 허용 필드만 정규화한다. id/status는 제외하고 나머지 필드와 내용을 포함한다. 객체 키 순서와 공백 차이는 무시하며 배열 순서는 유지한다.
- 재전달 시 내용 해시가 해당 세션에 존재해야 한다. 비어 있지 않은 ID가 함께 오면 item ID 소유권도 확인한다. 이전 응답에서 관찰하지 않은 이력을 새 입력 ID로 등록하지 않는다.
- 같은 텍스트가 여러 세션에서 독립적으로 생성될 수 있으므로 history 해시는 세션별 키로 저장한다. 다른 세션에서 동일 내용이 실제로 관찰되기 전에는 소유권이 없다. 응답/item/call ID의 기존 전역 소유권 정책은 유지한다.
- 클라이언트가 필드를 추가·삭제하거나 내용을 바꾸면 보수적으로 차단될 수 있다. ID/status 생략 외의 CLI 직렬화 차이를 추정하여 무시하지 않는다. 이 범위는 합성 로컬 검사로 검증하며 실제 CLI의 모든 이력 형태와 동일하다고 보장하지 않는다.
- additional_tools는 developer 역할, 완전한 비어 있지 않은 tools 배열 및 허용 필드(type/role/id/tools)만 인정한다. ID만 있는 정의 참조는 거절한다. 새 정의 ID는 ADR 0020과 동일하게 원자적으로 등록하고 기존 소유권 충돌은 차단한다.
- custom tool 결과, 이미지/파일 이력과 일반 런처는 후속 단계다. 기존 실패 요청의 자동 재전송 정책은 변경하지 않는다. 외부 의존성 추가 없음.

## 검증

실제 DB를 사용하는 로컬 테스트로 이력 내용 변경·교차 세션 차단, 동일 내용의 독립 관찰, 객체 키 순서 정규화, additional_tools ID-only/잘못된 역할/빈 정의 차단을 검사한다. 네트워크 포트나 모델 호출이 필요하지 않다.

```sh
go test -race -count=1 ./internal/affinity
go test -race -count=1 ./internal/proxy -run 'Test(HistoryContentAndSessionOwnership|AdditionalToolsRequireDefinitions|CLIInputClaimsOnlyValidatedContent|ContinuationContract)$'
```
