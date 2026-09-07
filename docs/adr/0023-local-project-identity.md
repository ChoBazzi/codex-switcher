# ADR 0023: 로컬 프로젝트·worktree 식별

- 상태: accepted
- 날짜: 2026-09-06
- 관련: ADR 0006, 0009, 0022

## 결정

런처에서 사용할 origin 산출을 `internal/projectidentity`로 분리한다. HTTP 헤더나 모델 출력에서 프로젝트 식별자를 가져오지 않는다.

- Git 프로젝트는 정규화한 common Git 디렉터리 경로로 식별한다. 연결된 worktree는 같은 프로젝트 해시를 공유하고, 각각의 정규화된 최상위 경로로 worktree를 구분한다. 별도 clone은 별도 프로젝트다.
- 브랜치는 전체 symbolic ref를 사용한다. 최초 커밋 전 브랜치도 지원하며 detached HEAD는 커밋 ID로 구분한다.
- Git이 없는 폴더는 호출자가 지정한 디렉터리 자체를 프로젝트 루트로 사용한다. 하위 폴더를 같은 프로젝트로 추정하지 않는다. 손상된 `.git`이 있으면 비 Git 프로젝트로 우회하지 않고 실패한다.
- 경로는 절대 경로 및 symlink 정규화 후 종류별 SHA-256으로 변환한다. 경로 이동은 새 식별자가 된다. 해시는 접근 권한이나 비밀값을 대신하지 않는다.
- Git 조회는 로컬 읽기 전용이며 5초 제한을 둔다. 상속된 `GIT_*` 환경 변수로 다른 저장소가 선택되는 것을 방지한다. 모델·네트워크·인증 조회는 없다.
- `switcher-helper project [directory]`는 해시만 출력하는 진단 명령이다. session은 비워 두며 CLI 시작 이벤트 전에는 세션을 생성하지 않는다.

## 검증 및 한계

일반/최초 커밋 전 브랜치, detached HEAD, 하위 디렉터리, symlink, 연결된 worktree, 환경 변수 오염, 비 Git 폴더, 손상된 Git 및 취소를 테스트한다.

```sh
go test -race ./internal/projectidentity ./cmd/switcher-helper
```

외부 의존성 추가 없음. Git 프로젝트에는 로컬 git 실행 파일이 필요하다. 일반 대화형 CLI 런처 연결과 실행 중 브랜치 변경 감지는 후속 작업이다. 메타데이터 조회는 여러 Git 명령에 걸친 원자적 스냅샷이 아니므로 런처는 작업 도중 변경된 origin을 조용히 재바인딩하면 안 된다.
