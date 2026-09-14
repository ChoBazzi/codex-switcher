# ADR 0017: SQLite 소유권 저장과 요청 의도 기록

- 상태: accepted
- 날짜: 2026-09-06
- 관련: ADR 0004, 0009, 0016

## 배경과 선택지

메모리 라우터만으로는 재시작 후 세션 계정과 continuation 소유권을 알 수 없다. Go SQLite 드라이버 추가, sqlite3 하위 프로세스 호출, 시스템 SQLite의 C API 직접 연동을 검토했다. V1이 macOS이며 Keychain에도 CGO를 사용하므로 시스템 `libsqlite3`를 선택한다. 외부 Go 모듈은 추가하지 않으며, 대신 작은 바인딩의 메모리·오류 처리를 직접 유지한다.

## 결정

- `internal/affinity`가 전용 0700 디렉터리의 0600 `affinity.sqlite3`를 관리한다. 디렉터리/파일 소유자·권한, DB 및 journal/WAL/SHM 심볼릭 링크·하드 링크를 검사하고 수명 전체에 독점 프로세스 잠금을 유지한다. 계정 로그인 잠금과 충돌하지 않도록 **별도 전용 하위 디렉터리**를 전달해야 한다.
- 실제 계정 식별자나 토큰이 아닌 a/b 슬롯과 프로젝트·worktree·브랜치·세션 ID, 응답 ID, 시각 및 상태만 저장한다. DB는 암호화하지 않으므로 로컬 메타데이터는 파일 권한으로 보호한다. 같은 사용자 권한의 악성 프로세스에 대한 완전한 방어를 보장하지 않는다.
- prepared statement의 bound parameter를 사용한다. DELETE journal, synchronous FULL, 외래키 및 명시적 트랜잭션을 사용한다. DB 잠금/쓰기 실패를 자동 재시도하지 않으며 원본 SQLite 오류 문자열은 출력하지 않는다. 초기 스키마 버전은 1이고 알 수 없는 버전은 거절한다.
- `Register`는 검증된 신규 대화에서만 호출한다. 기존 매핑은 덮어쓰지 않는다. `Lookup`은 차단된 세션의 소유권도 진단용으로 반환한다.
- `Begin`은 등록 origin과 모든 전달된 continuation ID의 소유 세션을 검사한 뒤, upstream 전송 전에 inflight 의도와 요청별 lease를 원자적으로 기록한다. 다른 계정뿐 아니라 같은 계정의 다른 세션 참조도 보수적으로 거절한다. 이 함수 자체는 HTTP 요청을 하지 않는다.
- `Finish`는 일치하는 lease만 처리한다. 성공 시 신뢰된 응답 관찰자가 전달한 ID 소유권과 ready 상태를 함께 저장한다. ID 충돌은 덮어쓰지 않고 트랜잭션 전체를 롤백한다. 실패는 blocked 상태로 남긴다. 저장 실패는 inflight 상태를 유지하여 추가 요청을 막는다.
- 재시작 시 남아 있는 inflight는 blocked로 복구한다. 전송 전에 종료되었더라도 처리 여부를 추정하지 않으므로 사용자 개입이 필요하다.
- GC는 마지막 요청 시각에서 **14일 초과** 유휴 상태만 삭제하며, inflight는 제외한다. 응답 소유권도 동일 트랜잭션에서 cascade 삭제한다. 경계의 정확히 14일 된 행은 다음 정리까지 남긴다. GC 이후 알 수 없는 continuation을 다른 계정에 재할당하지 않는다.
- `routing.NewPersistent`는 SQLite만을 매핑 저장소로 사용한다. 메모리 매핑과 이중 저장하지 않는다. 재시작 후 사용량은 새로 조회해야 하며, 기존 계정이 사전 선택 임계값을 넘었더라도 원래 매핑을 유지한다. 차단/진행 중 상태는 Resolve에서 거절한다.

## 구현 범위와 후속 작업

이 단계는 저장소와 내부 라우터 연동이다. ADR 0018에서 선택적 영속 프록시의 Begin/Finish 및 제한된 응답 ID 관찰을 연결했다. 기본 `routing.New` 및 기존 demo/live-test는 그대로이며 일반 CLI/상주 helper에는 아직 연결하지 않는다. 전체 CLI continuation 지원, 주기 GC, Wiki 예약과 스냅샷 영속화는 별도다.

CGO 비활성 또는 macOS 외 환경에서는 명시적 저장소 사용 불가로 실패하고 메모리/평문 파일 대체 저장으로 우회하지 않는다. DB 손상 시에도 자동 초기화하지 않는다.

## 검증

합성 데이터와 임시 디렉터리로 재열기, 종료된 inflight 복구, 실패 차단, ID 충돌 롤백, 소유권 격리, 14일 GC, 동시 admission 단일 승자, 파일 권한/링크 거절 및 영속 라우터의 재시작 동작을 검사한다.

```sh
go test -race -count=1 ./internal/affinity ./internal/routing
```

## 근거

- [SQLite 연결 API와 NOFOLLOW](https://www.sqlite.org/c3ref/open.html)
- [SQLite 트랜잭션](https://www.sqlite.org/lang_transaction.html)
