# ADR 0063: 로컬 도구 결과의 인라인 이미지 이력

- 상태: accepted
- 날짜: 2026-09-21
- 관련: ADR 0043, 0049, 0055, 0058

## 배경과 선택지

CLI가 스크린샷 등의 도구 결과를 `custom_tool_call_output.output`의 텍스트와 `input_image` 배열로 전달한다. 기존 텍스트 전용 검증은 이를 `tool_output_invalid`로 거절하고 HTTP `409 probe_tool_history_unsupported`를 반환한다. 과거 도구 결과도 다음 요청에 포함되므로 프록시 재시작이나 새 사용자 입력만으로는 해결되지 않는다.

이미지를 삭제하거나 텍스트로 대체하면 작업 문맥을 잃는다. 모든 이미지 URL과 file ID를 허용하면 계정에 종속된 서버 참조까지 전달할 수 있다. 따라서 이미지 데이터 자체가 포함된 제한된 형식만 보존한다. 이 결정은 ADR 0043의 이미지 미지원 중 **로컬 도구 결과의 인라인 이미지** 부분만 대체한다.

## 결정

- tools 경로의 `function_call_output`과 `custom_tool_call_output`에 텍스트와 `input_image`가 섞인 배열을 허용한다. 루트 및 보조 대화는 공통 검증을 사용한다.
- 이미지 필드는 `type`, 문자열 `image_url`, 선택적 `detail`만 허용한다. `detail`은 생략 또는 `auto`, `low`, `high`, `original`이다. null·잘못된 자료형·추가 필드는 거절한다.
- `image_url`은 `data:image/png;base64,`, `data:image/jpeg;base64,`, `data:image/webp;base64,`, `data:image/gif;base64,`의 정확한 접두사와 비어 있지 않은 정규 base64만 허용한다. CR/LF·공백·잘못된 padding/trailing bits·추가 MIME 매개변수는 허용하지 않는다.
- 외부 URL, 로컬 파일 경로, file ID, SVG 및 임의 opaque 필드는 계속 거절한다. 프록시가 이미지를 다운로드하거나 파일을 열거나 재인코딩하지 않는다. 검증은 전송 형식에 한정하며 실제 래스터 유효성과 모델별 detail 지원은 upstream이 판단한다.
- 전체 요청의 기존 4 MiB 제한을 유지하고 이미지 URL도 해독 검증 전 4 MiB로 제한한다. base64는 스트리밍 검증하며 전체 디코딩 사본을 보관하지 않는다. 이미지가 4 MiB 미만이어도 텍스트·JSON과 합친 요청이 제한을 넘으면 거절될 수 있다.
- 이미지·텍스트의 순서, 데이터, detail을 그대로 보존한다. 기존 도구 호출/결과 짝 검증, item ID 제거, 과거 call ID 치환, 현재 턴의 계정 고정과 실패 후 재전송 금지는 유지한다. 이미지가 새 사용자 입력 경계를 만들지 않는다.
- 일반 메시지/agent_message의 이미지 입력, 파일·서버 실행 도구·이미지 생성 SSE 출력까지 지원을 확대하지 않는다. CLI 대화 파일이나 checkpoint 형식도 변경하지 않는다. 새로운 의존성은 없다.

공식 [이미지 입력 문서](https://developers.openai.com/api/docs/guides/images-vision)는 base64 data URL과 detail을 설명한다. 이는 모든 Codex 전송 경로나 계정 간 파일 ID 호환성을 보증하지 않으므로 실제 설치 CLI와 합성 서버로 별도 검증한다.

## 검증과 적용

- `TestProbeToolsInlineImage*`: 혼합 텍스트/이미지 보존, MIME·detail·base64 검증, 미지원 참조·크기 초과 차단, 현재 턴의 타 계정 거절, 과거 도구 ID 치환 및 사용자 입력 경계 보존.
- `TestProbeToolsInlineImageDispatch`: 실제 루트 및 보조 handler에서 유효한 인라인 이미지 한 번 전달, 원격/잘못된 이미지 upstream 무호출 거절과 실패 요청 재전송 차단.
- `TestInstalledProbeToolsInlineImageHistory`: 설치 CLI → managed probe → 합성 upstream의 이미지 도구 왕복과 같은 대화 재개·계정 전환. 실제 계정·사용자 이미지·사용자 daemon은 사용하지 않는다.

CLI 0.155.1의 해당 경로는 이미지 detail을 생략하는 것으로 관측했다. 통합 검사는 이미지·텍스트 보존과 재개를, unit/API 검사는 명시적 `detail: high`의 무변경 전달을 각각 확인한다.

새 helper를 빌드한 뒤 CLI가 입력 대기 중일 때 앱·프록시를 함께 종료하고 새 helper를 사용하는 앱을 실행한다. 실패 상태가 남으면 복구 준비를 누르고 같은 CLI에서 새 지시를 입력한다. 새 세션을 생성하거나 이력을 지우지 않는다. 실행 중인 daemon은 바이너리를 다시 빌드하는 것만으로 교체되지 않는다.
