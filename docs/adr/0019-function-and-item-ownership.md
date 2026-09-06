# ADR 0019: 함수 호출과 item 소유권 확장

- 상태: accepted
- 날짜: 2026-09-06
- 관련: ADR 0018

## 배경과 선택지

도구 호출 이후 요청에는 response ID 외에 call_id와 item ID가 사용된다. 모든 ID를 동일한 참조로 취급하면 메시지 ID가 함수 호출 권한으로 오인될 수 있다. 따라서 종류별 내부 키와 기존 세션 소유권 검사를 결합한다.

## 결정

- 영속 프록시 입력에 `item_reference`, 문자열 output을 갖는 `function_call_output`, 이미 소유권이 확인된 `function_call` 재전달을 추가한다. 함수명·arguments의 변경 감지나 실행 검증 기능은 아니며, 소유 세션의 참조인지 검사하는 경계다.
- item 참조는 `item:<id>`, 함수 결과는 `call:function:<call_id>`로 SQLite에 저장·조회한다. 기존 응답 ID 키는 유지한다. 원본 ID에는 콜론/NUL을 허용하지 않아 내부 키와 충돌하지 않는다. 길이는 4000바이트 이하다. DB 스키마 변경은 없다. 과거 버전이 저장하지 않은 item/call ID는 소유권 미확인으로 차단한다.
- JSON/SSE의 최종 completed response.output에서 message/reasoning/function_call item ID 및 function_call의 call_id만 관찰한다. 함수명·arguments·결과 본문은 저장하지 않는다. 부분 item 이벤트만으로 소유권을 확정하지 않는다. 응답 ID와 추가 키들은 기존 Finish 트랜잭션으로 함께 저장된다.
- 다른 계정뿐 아니라 동일 계정의 다른 세션 참조도 차단한다. response ID·item ID·call ID의 서로 다른 용도 대입을 허용하지 않는다. 중복 JSON 키도 거절한다.
- custom_tool_call, 로컬 shell 전용 형식, 이미지/파일 결과, assistant 전체 이력, CLI additional_tools 확장은 아직 영속 경로에서 지원하지 않는다. 확인되지 않은 형식은 자동으로 허용하지 않는다. 기존 live-test의 별도 additional_tools 지원은 유지한다.

## 검증 및 범위

- 합성 JSON/SSE 함수 호출 → DB 재열기 → 원래 세션의 결과 전송 성공을 확인한다. 교차 계정·교차 세션·잘못된 종류·미확인 참조·부분 스트림을 차단한다. 모델이나 실제 함수를 실행하지 않는다.
- 설치된 CLI에 존재하지 않는 합성 함수 호출을 반환하여 `function_call_output`의 call_id와 문자열 output이 다음 요청에 포함됨을 확인한다. 이 검사는 직접 합성 서버 경로이며 영속 프록시 전체 CLI 호환성 검증이 아니다.
- 실제 CLI 런처, 모든 도구 유형, 다중 작업 인계는 여전히 후속 단계다. 네트워크 실패 자동 재시도 정책은 변경하지 않는다. 외부 의존성 추가 없음.

```sh
go test -race -count=1 ./internal/proxy
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 ./internal/cliprobe -run TestInstalledCodexFunctionOutput
```

## 근거

[OpenAI 함수 호출 문서](https://developers.openai.com/api/docs/guides/function-calling)의 function_call_output/call_id 연결을 참고했다. 공개 Responses 형식과 Codex backend의 모든 확장을 동일하게 취급하지 않는다.
