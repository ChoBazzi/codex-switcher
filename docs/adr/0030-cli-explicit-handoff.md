# ADR 0030: CLI 명시적 인계 예약·취소·신규 실행

- 상태: accepted (읽기 전용 단발 CLI 연결)
- 날짜: 2026-09-07
- 관련: ADR 0009, 0028, 0029

## 결정

`handoff prepare -C DIR --conversation HANDLE --checkpoint ID --to a|b --confirm-boundary`는 정상 완료된 원본 대화와 최신 등록 후보를 검증한다. 사용자는 미해결 작업이 없고 Wiki가 현재 작업 상태를 나타냄을 명시적으로 확인한다. 자동 도구 결과/안전 경계 판정기를 구현한 것은 아니다. 필요하면 먼저 --checkpoint로 새 Wiki를 작성해야 하며 prepare는 작성 모델 호출을 하지 않는다.

prepare는 Wiki를 로컬 재검증·백업하고 별도의 private Application Support `handoff-snapshots/<SHA256>.json` 파일에 고정한다. 고정 파일은 최근 3개 백업 순환과 독립적이며 기존 파일이 다르면 덮어쓰지 않는다. 예약 등록 전 저장 실패는 예약을 만들지 않는다. 예약 실패 후 고정 파일이 남을 수 있으며 자동 GC는 아직 없다. 대상 계정의 사용량 메타데이터를 한 번 조회하고 예약한다. CLI 생성/모델 호출은 하지 않는다.

대기 중 원본 Readiness 검사도 거절하여 exec가 원본 ready 기록을 지우기 전에 보류를 알린다. `handoff cancel -C DIR --conversation HANDLE --id RESERVATION`은 네트워크/모델 요청 없이 대기 예약만 취소한다. 소비된 예약은 취소하지 않는다. 취소된 예약 ID는 복구하지 않으며 고정 파일은 삭제하지 않는다.

`exec -C DIR --handoff RESERVATION USER_PROMPT`는 명시된 새 사용자 입력이 있을 때만 시작한다. --resume/--checkpoint와 병용하지 않는다. 예약 원본과 현재 프로젝트/worktree/브랜치 일치, 미소비 여부, 고정 파일의 메타데이터·해시를 먼저 검사한다. 새 private CLI home을 생성하고 기존 대화 파일을 복사하거나 CLI resume 인수를 사용하지 않는다. 모델 선택은 새 exec의 기본값 또는 명시한 --model을 따른다.

전달 프롬프트에는 원본 메타데이터 행과 완료 마커를 제거한 Wiki 본문 및 새 사용자 입력만 포함한다. Wiki는 참고 데이터로 표시한다. 본문에 알려진 원본 session ID가 남아 있으면 거절한다. 요약에 임의로 포함된 모든 비밀정보/식별자를 완전 탐지하는 보장은 아니며 작성 단계의 내용 검토는 여전히 필요하다. 기존 대화 전체나 previous_response_id는 생성하지 않는다.

대상의 최신 사용량/인증과 신규 thread.started를 확인한 뒤 신규 세션만 대상에 연결한다. 실패 시 일반 신규 세션이나 다른 계정으로 우회하지 않는다. 소비 후 CLI 실행 실패는 예약을 되돌리지 않는다. 고정 Wiki 수정, 소비된 예약, 잘못된 origin은 실패로 종료한다.

예약 중이거나 이미 소비된 checkpoint ID의 recheck는 DB 조회로 거절한다. 예약 취소 후에는 기존 후보 재검사를 허용한다. 체크포인트 검사와 모델 실행 모두 affinity → conversation 순서의 잠금을 사용한다.

## 검증 및 남은 범위

임시 HOME/합성 인증·사용량으로 prepare의 모델 호출 없는 예약과 cancel의 사용량 조회 없는 취소를 검증한다. 실제 설치된 CLI와 합성 upstream으로 신규 세션이 B에 연결되고 원본 A가 유지되며, 요청에 Wiki/새 입력만 있고 원본 session ID·메타데이터가 없는지 검사한다. 실계정 API는 테스트에서 사용하지 않는다.

일반 TUI 프로세스 유지 전환, 메뉴바 버튼, 자동 한도 전환, 사용자 안전 경계 확인을 대체할 도구 상태 검증, 예약 목록/고정 파일 정리는 후속 작업이다. 실계정 간 인계는 사용자 검증이 필요하다. 외부 의존성 추가 없음.
