# 0003. 프록시 incoming 인증 및 통신 프로토콜 범위

- 상태: accepted
- 날짜: 2026-09-05

## 결정 배경

Codex CLI와 로컬 프록시(`127.0.0.1:<port>`), 그리고 프록시와 OpenAI Upstream 간의 통신 프로토콜과 인증 처리 방식을 명확히 정의해야 한다.
로컬 환경의 보안성, 공식 CLI와의 무간섭 호환성, 그리고 한도 도달 시의 결정론적 fail-closed 전환 제어가 핵심 고려사항이다.

## 검토한 선택지

### 1. incoming HTTP 인증 방식 (CLI ➡️ 프록시)
- **선택지 1 (Drop & Inject, 선택)**: `127.0.0.1` 루프백 인터페이스 자체를 신뢰 경계로 두고, CLI가 보낸 incoming `Authorization` 헤더는 내용과 무관하게 폐기한 뒤 Keychain의 유효한 계정 토큰으로 교체(Inject)하여 Upstream에 전달.
- **선택지 2 (로컬 공유 시크릿 검증)**: 프록시 시작 시 고유 Bearer 토큰을 발급하고 CLI가 이를 헤더로 보내도록 강제. (공식 CLI의 인증 저장소 구조상 설정 우회가 까다로워 기각)

### 2. V1 통신 프로토콜 범위
- **선택지 1 (HTTP POST + SSE 전용, 선택)**: 프록시는 오직 HTTP 요청과 SSE(Server-Sent Events) 스트리밍만 중계. WebSocket은 V1 범위에서 제외.
- **선택지 2 (WebSocket 터널링 및 프레임 인터셉트)**: 지속 연결(Persistent Connection) 내에서 실시간 프레임을 파싱해 429 감지 및 계정 전환 수행. (구현 복잡도가 지나치게 높고 연결 해제/재연결 상태 머신이 불안정하여 기각)

## 최종 결정

1. **incoming HTTP 인증은 루프백 신뢰 기반 "Drop & Inject"를 채택한다.**
   - CLI ➡️ 프록시 구간은 루프백 격리(`127.0.0.1`)를 신뢰한다.
   - CLI의 incoming `Authorization` 헤더는 즉시 제거(Strip)되며 로그에 기록하지 않는다.
   - 프록시는 활성 계정의 유효 Access Token을 주입하여 Upstream으로 전달한다.
   - 단, 메뉴바 앱과의 제어 통신을 위한 Control API(`/control/*`)는 **로컬 Bearer Secret**으로 보호한다.

2. **V1 프로토콜 범위는 HTTP POST + SSE(Server-Sent Events)로 한정한다.**
   - V1은 텍스트 코딩 작업의 다음 세션 인계에 집중하며 준비 후 사용자 입력을 기다린다 (ADR 0009).
   - 실패 요청은 재시도하지 않는다. HTTP 응답 시작 전과 SSE 시작 후의 오류 전달을 구분하며 이미 전달된 응답의 롤백은 보장하지 않는다.
   - CLI의 모델 요청을 HTTP/SSE로 제한하는 설정과 실제 전송 방식을 검증한다. WebSocket은 V1에서 중계하지 않는다.

## 예상되는 결과와 장단점

### 장점
- 공식 Codex CLI와의 인증 및 전송 호환성은 지원 버전별로 검증한다.
- HTTP 단위의 명확한 상태 전이로 계정 스위칭 시 버그 및 데이터 오염 위험이 최소화된다.
- Go 표준 라이브러리(`httputil.ReverseProxy`)로 매우 가볍고 안정적인 프록시 구현이 가능하다.

### 단점
- WebSocket 전용 특수 기능이 향후 추가될 경우 프록시에서 즉시 수용할 수 없으며 V2 과제로 확장해야 한다.
