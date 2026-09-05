# 0007. 앱 메타데이터, CLI 호환 버전 및 로컬 배포 서명

- 상태: accepted
- 날짜: 2026-09-05

## 결정 배경

macOS 애플리케이션 식별자, 지원할 공식 Codex CLI의 최저 버전 기준, 그리고 V1 개발 및 실행을 위한 코드 서명(Code Signing) 방식을 확정해야 한다.

## 검토한 선택지 및 최종 결정

### 1. 앱 명칭 및 Bundle Identifier
- **결정**: 
  - 앱 이름: `Codex Switcher` (실행 프로세스명: `CodexSwitcher`)
  - Bundle Identifier: `com.bazzi.codex-switcher`
- **선정 이유**: macOS Keychain의 Access Group 및 알림 센터, 설정 파일 등에서 일관되고 충돌 없는 식별자로 활용.

### 2. 지원할 공식 Codex CLI 최소 버전
- **결정**: `0.150.0` 이상
- **선정 이유**: 프로필 계층 설정 기능(`-p/--profile`) 및 `CODEX_HOME` 격리 브라우저 인증이 안정적으로 지원되는 버전으로 설정하여 호환성을 보장함. (현재 검증 환경: `0.153.2`)

### 3. V1 앱 배포 및 코드 서명 방식
- **결정**: 로컬 개발자 Ad-hoc 서명 (Self-signed)
- **선정 이유**: V1은 단일 사용자의 로컬 macOS 환경에 집중하므로, 연회비가 수반되는 Apple Developer Program 등록 및 Notarization(공증) 절차 없이 로컬 Mac 환경에서 `swift build`로 즉시 빌드 및 실행할 수 있는 Ad-hoc 서명을 채택함. (외부 배포용 공증은 필요시 V2에서 확장)

## 예상되는 결과

- 외부 인증서 발급이나 복잡한 공증 파이프라인 없이 로컬에서 완전한 개발 및 디버깅이 가능하다.
- 명확한 Bundle ID를 통해 macOS Keychain의 격리된 공간에 OpenAI 토큰을 안전하게 보관할 수 있다.
