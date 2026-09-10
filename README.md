# Codex Switcher

macOS 메뉴바에서 여러 Codex 계정의 사용량과 한도를 확인하고, 현재 계정의 한도가 소진되면 로컬 역방향 프록시를 통해 사용 가능한 계정으로 안전하게 전환하는 도구입니다.

## Status

계정 카드에 서버가 제공한 요금제를 표시합니다. 요금제를 받지 못하면 `요금제 미확인`, 이전 값이면 `이전 조회 정보`로 표시합니다. **등록된 계정은 사용량이 ‘확인 필요’여도 로그아웃할 수 있습니다.** 실제 요청·인증 작업 중 차단은 유지합니다. [ADR 0052](docs/adr/0052-account-plan-display.md)

계정 목록은 **620pt 창의 2열 × 최대 3행**으로 표시합니다. 선택 계정에 배경색과 테두리를 표시하며 로그아웃 확인은 목록 아래에 나타납니다. **새로고침은 프록시가 실제 사용량을 즉시 조회**합니다. 이미 조회 중이면 그 결과를 기다리며, 완료 후 5초 이내 연타는 제한합니다. 다음 자동 조회는 완료 후 60초입니다. 새 앱과 helper를 함께 재시작하세요. [ADR 0050](docs/adr/0050-account-grid-and-cache-refresh-feedback.md), [ADR 0051](docs/adr/0051-explicit-usage-refresh.md)

계정 한도 없이 화면을 확인하려면 `swift run --package-path macos CodexSwitcher --demo`로 합성 계정 5개를 표시할 수 있습니다. 데모에서는 로그인·전환·새로고침을 실행하지 않습니다. 실제 앱에서는 마지막 조회 완료 5초 후 새로고침을 누르고 성공 계정의 조회 시각과 완료 문구가 갱신되는지 확인하세요. 실패 계정의 이전 값은 오래된 정보로 표시합니다. 조회는 모델 요청을 만들지 않습니다.

**대화 압축:** 현재 Codex CLI 0.153.4의 프록시 프로필은 텍스트 요약 경로를 사용하며, 설치 CLI 합성 검사에서 **A 압축 → 같은 대화 B 전환 → 요약 보존**을 확인했습니다. native `/responses/compact` JSON 경로도 추가했지만 **암호화된 압축 결과는 생성 계정에 고정**됩니다. 암호화 결과의 계정 간 호환성은 미검증입니다. [ADR 0049](docs/adr/0049-compaction-and-account-ownership.md)이 아래 과거 '압축 미지원' 설명에 우선합니다.

실계정 없는 압축 검사:

```sh
cd /Users/bazzi/dev/work/my
SWITCHER_CODEX_INTEGRATION=1 GOCACHE=/private/tmp/codex-switcher-go-build GOMODCACHE=/private/tmp/codex-switcher-go-mod go test -race -count=1 -timeout 90s ./cmd/switcher-helper -run 'TestInstalledProbeCompaction|TestProbeNativeCompaction|TestProbeCompactOwnership'
```

실사용 확인은 앱/helper를 새 빌드로 재시작하고 새 연결 명령으로 CLI를 연결한 뒤 진행합니다. A에서 합성 기억 문구와 간단한 계획을 대화에 남기고, 턴이 끝난 뒤 `/compact`를 실행하세요. 압축 완료 후 B로 전환하고 이전 기억 문구/계획을 도구 없이 질문합니다. 실제 모델 사용량이 발생하며 요약은 세부사항을 완전히 보존한다는 보장이 없습니다. native 암호화 경로가 사용되면 다른 계정 전환이 거절되는 것이 현재 안전 정책입니다.

CLI 도구를 Esc로 취소한 뒤에도 `CLI 도구 결과 대기`로 잠겨 있으면, 입력창으로 돌아왔는지 확인하고 앱의 **CLI 중단 확인 · 전환 잠금 해제**를 누르세요. 그다음 다른 사용 가능한 계정을 선택하고 CLI에 새 지시를 입력합니다. HTTP 요청 중에는 해제할 수 없으며 이 버튼은 실행 중인 로컬 도구를 종료하지 않습니다. 불완전한 도구 이력은 새 대화가 필요할 수 있습니다. [ADR 0048](docs/adr/0048-explicit-tool-turn-abandonment.md)

최대 **5개 계정**을 등록할 수 있습니다. 앱의 **계정 추가**로 첫 빈자리(A~E)에 브라우저 로그인하며, 로그아웃 성공 후 그 자리를 다시 사용할 수 있습니다. 만료·소진 계정도 정원에 포함되며 미확인 상태는 빈자리로 취급하지 않습니다. 기존 A/B 인증은 유지됩니다. 새 앱과 helper를 함께 빌드하고 재시작하세요. [ADR 0047](docs/adr/0047-five-account-capacity.md)이 아래 과거 A/B 전용 설명에 우선합니다.

수동 검증: `계정 추가`로 서로 다른 계정을 최대 5개 등록한 뒤 추가 버튼이 비활성화되는지 확인합니다. 한 계정에서 `로그아웃 → 취소` 시 정원이 그대로인지, 실제 `로그아웃 확인` 성공 후 카드가 사라지고 추가가 다시 가능한지 확인합니다. 다시 추가하면 다른 계정의 슬롯은 유지되고 빈 슬롯만 재사용합니다. C~E도 사용량 확인 후 전환할 수 있어야 합니다. 계정 등록 자체는 모델 호출을 시작하지 않습니다. 기존 CLI 작업 중에는 먼저 턴을 완료하세요.

브라우저 로그인 창을 닫기만 하면 OAuth 대기는 계속됩니다. 앱의 계정 목록 위에 표시되는 **계정 추가 / 로그인 취소**를 누르세요. helper 종료와 저장 상태 확인 후 다시 추가할 수 있습니다. 직접 취소하지 않으면 최대 약 5분 후 자동 취소됩니다.

사용량은 **앱 관리 프록시만 조회**하고 앱은 같은 결과를 표시합니다. 자동 조회는 완료 후 60초, 수동 새로고침은 즉시 조회입니다. 로그인·로그아웃 후 기존 값은 즉시 무효화하고 다음 정기 또는 명시적 수동 조회에서 갱신합니다. [ADR 0051](docs/adr/0051-explicit-usage-refresh.md)

실패 후에도 **다른 계정을 수동 선택**할 수 있습니다. 선택만으로 실패 요청을 재전송하지 않으며, 전환 후 CLI에 새 지시를 입력해야 합니다. 예: `현재 파일 상태부터 확인하고, 아직 안 된 작업만 수행해줘.` 미완료 도구 이력·압축이 남아 있으면 새 대화가 필요할 수 있습니다. [ADR 0045](docs/adr/0045-explicit-manual-recovery.md)

로컬 개발 도구 전환 실험을 앱에 연결했습니다(`--tools`). function/custom 도구 호출·텍스트 결과를 보존하고 **도구 실행을 포함한 턴이 끝난 뒤** 계정을 전환합니다. 압축·이미지·서버 실행 도구는 아직 지원하지 않습니다. 아래 텍스트 전용 설명은 이전 실험 기록이며 최신 범위는 [ADR 0043](docs/adr/0043-local-tool-history-switching.md)을 따릅니다.

### 개발 도구 전환 검증

완료 이벤트/연결 종료 시점 차이로 후속 요청이 거절되던 문제를 수정했습니다. 새 helper로 재시작해야 적용됩니다. 종료 지연·취소 회귀 검사는 `go test -race ./cmd/switcher-helper -run 'TestProbeCompletionOverlap|TestProbeTerminalGate'`입니다. 실제 요청 실패 후 세션 차단은 유지됩니다. [ADR 0044](docs/adr/0044-probe-completion-delivery-order.md)

실계정 없는 전체 검사:

```sh
cd /Users/bazzi/dev/work/my
sh macos/Checks/verify.sh
```

설치된 CLI의 도구 왕복만 검사:

```sh
cd /Users/bazzi/dev/work/my
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v ./cmd/switcher-helper -run 'TestInstalledProbeTools|TestProbeTools|TestProbeTurn'
```

실계정 수동 검사(모델 사용량 발생):

1. 기존 CLI 작업을 끝내고 앱을 종료한다. 위 전체 검사로 새 helper·앱 빌드를 확인한 후 아래 명령으로 앱을 실행한다.

   ```sh
   cd /Users/bazzi/dev/work/my
   swift run --package-path macos CodexSwitcher --helper /Users/bazzi/dev/work/my/bin/switcher-helper
   ```

2. A/B 모두 최신 잔여량이 5% 초과인지 확인한다. 별도 터미널에서 빈 테스트 폴더를 만든다.

   ```sh
   switcher_test_dir=$(mktemp -d /private/tmp/switcher-smoke.XXXXXXXX)
   cd "$switcher_test_dir"
   ```

3. 앱의 `Codex CLI 연결 명령 복사`를 붙여넣고, 실행 전 명령 끝에 `--sandbox workspace-write --ask-for-approval on-request`를 붙인다. 기본 프록시 프로필은 read-only이므로 파일 수정 검사는 명시적 권한 설정이 필요하다. 기존 프로젝트나 기존 설정은 변경하지 않는다.
4. CLI에 요청한다: `현재 폴더에 switcher-smoke.txt를 만들고 첫 줄에 파란사과를 한 번만 기록해줘. 파일을 읽어 확인하고 작업을 끝내줘.` 필요하면 CLI에서 작업 권한을 검토한다. 도구 처리 중 전환 버튼이 비활성화되고 최종 답변 후 활성화되는지 확인한다.
5. 앱에서 반대 계정으로 전환한다. 클릭만으로 모델 요청·파일 변경이 시작되면 안 된다. 같은 CLI에서 `이전 대화의 도구 결과만 참고해 방금 만든 파일명과 내용을 알려줘. 도구를 다시 실행하지 마.`를 요청한다. 파일명·파란사과와 새 계정 표시를 확인한다.
6. 이어서 `같은 파일의 마지막에 전환성공을 한 줄만 추가하고 읽어서 확인해줘.`를 요청한다. 터미널에서 `cat switcher-smoke.txt`로 파란사과와 전환성공이 각각 한 줄인지 확인한다. 이 테스트는 실제 파일 수정/문맥 확인이지 모델의 중복 명령 생성 가능성을 일반적으로 보장하는 검사는 아니다.

실패 시 자동 재시도하지 말고 고정 오류 코드만 공유한다. `probe_tool_history_unsupported`는 미지원/불완전 이력, `probe_tool_response_unsupported`는 완료 판별 불가다. `/compact`·이미지·서버 도구는 이 테스트에서 제외한다. 취소로 최종 응답이 오지 않을 때 턴 잠금이 남는 문제는 후속 범위다. 테스트 파일은 자동 삭제하지 않으며 위 임시 폴더에 남는다.

자동 전환 기준은 잔여량 **5% 이하**입니다. 다음 요청부터 5% 초과인 다른 계정을 선택합니다. 계정 카드의 기존 `A/B로 전환` 버튼으로 수동 전환할 수 있으며 대상도 5% 초과여야 합니다. 둘 다 기준 이하이면 요청을 차단합니다.

현재 개발 범위는 **사용량 조회와 계정 전환**입니다. Wiki는 앱에서 분리하고 기존 기록과 Go 명령은 보존했습니다. 앱 프록시는 첫 요청에서 잔여량이 큰 계정을 선택하고, 이후 현재 계정의 한도 소진이 확인되면 다음 요청부터 전환합니다. 수동 선택은 한도가 남은 동안 유지합니다. 오래된 사용량은 판단에 쓰지 않으며 실패 요청을 자동 재전송하지 않습니다. 단일 CLI·텍스트 전용입니다. [ADR 0042](docs/adr/0042-quota-selection-at-request-boundary.md)

기본 검증은 `sh macos/Checks/verify.sh`, 보존된 Wiki 하네스 검증은 `sh macos/Checks/verify-wiki.sh`입니다. 아래 과거 Wiki UI 설명은 현재 앱 사용법이 아닌 이전 구현 기록입니다.

2026-09-09: 사용자가 텍스트 전용 실험에서 같은 Codex CLI의 A→B 전환과 문맥 유지를 확인했습니다. 앱의 기본 전환 버튼도 Wiki 없이 다음 요청의 계정을 선택하도록 연결했습니다. **실제 앱 버튼의 수동 검증은 남아 있습니다.** 일반 도구·reasoning·압축·다중 세션은 추가 검증 대상입니다. [ADR 0040](docs/adr/0040-same-conversation-switch-and-optional-wiki.md)이 이전 Wiki 필수 전환 설명에 우선합니다.

현재는 Phase 0 구현 단계입니다. Go 기반 Wiki 로컬 검증, 메모리 기반 세션 인계, 단일 시도 HTTP/SSE 프록시와 합성 데모가 있습니다. 실제 CLI 0.153.4로 합성 응답·오류·대화 ID 격리를 검증했습니다. 브라우저 로그인·Keychain 저장, 실제 모델 응답과 사용량 조회는 사용자 환경에서도 확인했습니다. 읽기 전용 단발 `exec`에 프로젝트 식별·사용량 기반 계정 선택·SQLite 영속 프록시를 연결하고 합성 CLI 테스트를 통과했습니다. 이 실행 경로의 실계정 검증, SwiftUI, 일반 대화형 CLI 연결, 토큰 자동 갱신 및 Codex Wiki 인계 연동은 아직 남아 있습니다.

## Local development

### CLI 연결 목표와 현재 지원 범위

목표는 **기존 Codex CLI 대화형 화면 → 로컬 프록시 → OpenAI**입니다. Codex의 입력창·슬래시 명령·도구 실행을 그대로 사용하고 프록시가 계정 선택과 인증을 담당합니다. 별도 채팅 런처로 입력을 대신 받지 않습니다.

잘못 추가했던 `switcher-helper chat`과 미검증 `--workspace-write` 옵션은 철회했습니다. 기존 `exec`는 검증 및 명시적 Wiki 인계 경로로 유지하며 최종 대화형 사용 방식이 아닙니다.

직접 프로필·훅 연결의 첫 단계를 추가했습니다. 서버가 Codex를 실행하지 않으며 실제 TUI에서 훅 승인 후 실계정 대화 검증은 남아 있습니다. 같은 화면의 자동 계정 전환·Wiki 인계는 아직 지원하지 않습니다. [ADR 0038](docs/adr/0038-direct-cli-profile-and-hooks.md)

### Codex 직접 프록시 연결

#### 앱에서 자동·수동 전환 (현재 텍스트 전용)

```sh
cd /Users/bazzi/dev/work/my
go build -o bin/switcher-helper ./cmd/switcher-helper
swift run --package-path macos CodexSwitcher --helper /Users/bazzi/dev/work/my/bin/switcher-helper
```

1. A/B 로그인 완료 상태에서 앱이 모델 프록시도 자동 실행합니다. 시작 실패 시 계정을 연결한 후 `모델 프록시 재시작`을 누르세요. 상태 서버와 모델 프록시는 별개입니다.
2. `Codex CLI 연결 명령 복사`를 누르고 원하는 프로젝트의 터미널에 붙여넣어 직접 실행합니다. 별도 switch-probe 서버나 훅 승인은 필요 없습니다.
3. 사용량 조회가 완료된 뒤 기억할 단어를 입력합니다. 첫 요청은 잔여량이 큰 계정으로 전달되고 앱에 처리 계정이 표시됩니다. 첫 요청 전 기본 A 표시는 자동 선택 결과가 아닙니다.
4. 답변이 끝난 뒤 앱에서 반대 계정으로 전환하고 선택을 확인합니다. 클릭만으로 Wiki·모델 요청이 시작되면 안 됩니다.
5. 같은 CLI에서 기억 질문을 입력해 문맥과 선택 계정 처리를 확인합니다. 이후 현재 계정의 한도가 소진된 것으로 조회되면 다음 요청부터 사용 가능한 다른 계정으로 자동 전환합니다. **실제 사용량이 발생합니다.**

자동 선택은 프록시의 60초 사용량 조회 결과를 사용합니다. 첫 조회 전이나 최신 사용량 확인 불가 시 요청을 거절할 수 있습니다. 응답 도중 한도 오류가 나면 자동 복구·재전송하지 않습니다. 앱과 helper를 새 빌드로 재시작해야 적용됩니다.

버튼은 요청 처리 중·실패·미연결·선택 ACK 대기·오래된/부족한 사용량에는 비활성화됩니다. 단일 CLI/텍스트 전용이며 일반 도구 작업 지원이 아닙니다. 프록시 재시작 시 새 연결 명령으로 CLI를 다시 시작해야 합니다. Wiki 인계 버튼은 현재 앱에서 제거했습니다. 아래 과거 Wiki 기본 버튼 설명은 이 절차로 대체합니다.

#### 실험용 수동 계정 전환 (텍스트만)

assistant 답변의 `phase`(commentary/final_answer)를 검증 후 보존합니다. phase를 포함한 합성 응답 → CLI 재개 → B 요청의 보존 여부도 검사합니다.

기본 모델이 input에 포함하는 `additional_tools` 선언을 처리하도록 수정했습니다. 텍스트 전용 실험에서는 선언을 제외하며 서버 참조·도구 실행 이력의 차단은 유지합니다. 설치된 CLI 기본 모델의 첫 요청/재개와 합성 A→B 인증 변경을 다음 명령으로 검증합니다(실제 모델 호출 없음).

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v ./cmd/switcher-helper -run 'TestInstalledProbeParser|TestProbe'
```

`./bin/switcher-helper switch-probe --allow-live`를 실행합니다(A/B 등록 필요). 출력의 `codex_home` 값을 복사해 다른 터미널에서 `CODEX_HOME='출력된 경로' codex`를 직접 실행합니다. 기존 로그인·설정은 변경하지 않습니다. A로 짧은 기억 요청을 보내고 답변과 `probe_request_finished`를 기다린 뒤, **서버 터미널**에 `b`와 Enter를 입력하세요. `accepted:true` 후 **같은 Codex 화면**에서 기억 질문을 보냅니다. 두 요청의 슬롯 a/b와 실제 답변을 함께 확인합니다. 모델 사용량이 발생합니다.

훅·Wiki·앱 연결 없이 별도로 실행하는 제한적 실험입니다. message id 제거와 도구 비활성화가 포함되므로 인증만 바꾸는 완전 투명 프록시 검증은 아닙니다. reasoning/도구/서버 continuation 참조는 `probe_requires_plain_text_history`로 중단됩니다. 요청 중 전환과 실패 후 재시도는 거절합니다. 종료는 서버 Ctrl+C이며 임시 config는 제거하지만 CLI 대화 기록은 출력된 임시 디렉터리에 남습니다. [ADR 0039](docs/adr/0039-text-only-account-switch-probe.md)

먼저 기존 helper exec가 끝난 상태에서 서버를 실행하세요. A/B 로그인은 미리 완료해야 합니다. 서버는 사용량을 조회하지만 모델을 호출하지 않습니다.

```sh
cd /Users/bazzi/dev/work/my
go build -o bin/switcher-helper ./cmd/switcher-helper
./bin/switcher-helper direct-proxy -C .
```

`direct_proxy_ready`가 나오면 **다른 터미널의 동일 프로젝트에서** 직접 실행합니다.

```sh
cd /Users/bazzi/dev/work/my
codex -p switcher
```

1. Codex 화면의 `/hooks`에서 Switcher의 `direct-hook` 명령 3개를 검토하고 승인합니다. 기존 다른 훅을 일괄 승인하지 마세요.
2. `/new`로 새 대화를 열어 승인된 SessionStart 훅을 실행합니다.
3. `안녕이라고만 답해줘. 도구는 사용하지 마.`를 입력합니다. **이 단계는 실제 모델 사용량을 소모합니다.**
4. Codex 답변과 서버의 `direct_proxy_request_finished` 진단을 확인합니다. `upstream_attempts`가 증가하고 `completion_committed: true`인지 확인하세요. 이 진단은 누적 값이며 CLI 전체 턴 성공 판정과는 다릅니다.

서버가 실행 중이어야 하며, 앱이 실행하는 상태 조회 서버만으로는 연결되지 않습니다. native CLI 세션의 앱 표시·전환 버튼은 아직 연동하지 않았습니다. 서버의 affinity 잠금 때문에 기존 helper exec/로그아웃/Wiki 인계는 서버를 종료한 뒤 사용해야 합니다.

기존 CODEX_HOME(기본 ~/.codex)에 전용 profile과 private 연결 파일 두 개만 생성하며 기존 config.toml과 로그인은 변경하지 않습니다. 폴더는 미리 존재해야 합니다. `--profile-dir PATH`를 지정한 경우 CLI에서도 동일한 CODEX_HOME을 사용하세요. 같은 이름의 파일이 있으면 덮어쓰지 않고 `direct_profile_install_failed_existing_or_inaccessible`로 중단합니다. 강제 종료 뒤 잔류한 파일에는 로컬 비밀값이 있으므로 내용을 공유하지 마세요. 정상 종료는 생성 당시 그대로인 파일만 제거합니다. 서버를 재시작하면 CLI도 재시작해야 합니다.

실계정 없는 직접 연결 검사:

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v ./internal/directcli
```

### 신규 세션 등록 진단 (2026-09-08)

일반 신규 세션은 잔여량이 10% 이하라도 최신 사용량·인증이 유효하고 잔여량이 양수이면 시작할 수 있습니다. 명시적 인계 대상은 기존 10% 초과 기준을 유지합니다. 자동 체크포인트 구현과는 별개입니다. [ADR 0036](docs/adr/0036-new-session-quota-and-registration-diagnostics.md)

`cli_stage: session_registration` 실패에는 `session_registration_code`가 추가됩니다. `routing_account_unavailable`은 배정 가능한 계정 없음, `routing_usage_unavailable`은 사용량 확인 불가, `routing_credentials_unavailable`은 로컬 인증 확인 실패입니다. `affinity_storage_unavailable`, `cli_record_registration_failed`, `cli_session_binding_unavailable`은 각각 저장소·대화 기록·바인딩 문제입니다. 원문 오류나 인증 정보는 출력하지 않습니다.

`sh macos/Checks/verify.sh`는 실계정 호출 없이 수동 전환의 Wiki 작성·입력 대기·실행·취소·재확인·재시작 복원을 검사합니다. 실제 앱에서의 계정 전환 확인은 아래 '앱 전환 통합 검증' 절차를 별도로 수행하세요.

### macOS 메뉴바 UI (첫 단계)

SwiftUI 메뉴바 UI를 추가했습니다. 계정 사용량과 실제 exec 세션 상태를 연결하고 활성 세션 카드 전체 배경을 강조합니다. 정상 종료 세션에서 Wiki 작성·재확인·계정 인계 준비·취소·새 입력 실행을 지원합니다. 아래 내용이 기존 단계별 Status의 SwiftUI 미구현 설명을 대체합니다.

```sh
cd /Users/bazzi/dev/work/my
swift run --package-path macos CodexSwitcher --demo
```

상단 메뉴바의 `A · 데모`를 클릭하고 상태 선택기를 바꾸어 활성(진한 파란 배경), Wiki 준비, 사용자 입력 대기, 실패 화면을 확인합니다. 데모는 실제 로그인·모델 호출·전환을 하지 않습니다. 라이트/다크 모드에서도 활성 카드 글씨가 잘 읽히는지 확인하세요.

실제 사용량을 보려면 데모를 종료한 뒤:

```sh
cd /Users/bazzi/dev/work/my
go build -o bin/switcher-helper ./cmd/switcher-helper
swift run --package-path macos CodexSwitcher --helper /Users/bazzi/dev/work/my/bin/switcher-helper
```

두 계정 사용량을 즉시 조회한 후 60초마다 갱신합니다. Keychain 접근 승인이 나타날 수 있습니다. 새로고침 버튼으로 수동 조회할 수 있습니다. helper 인수를 생략하면 실행 방법을 안내합니다. 앱 내 helper 선택/변경 버튼은 없습니다. 2분 지난 데이터는 오래된 정보로 표시되며, 오류는 0%로 바꾸지 않습니다. 현재는 앱 번들 설치가 아닌 터미널 실행 방식이며 helper 경로는 재실행 시 다시 지정합니다.

### 실제 CLI 세션 상태 연결

계정 카드의 **로그아웃**을 누르면 별도 팝업 대신 카드 안에 **취소 / 로그아웃 확인**이 표시됩니다. 확인해야 선택 슬롯의 저장된 인증을 제거하며, 취소하거나 패널을 닫으면 인증을 변경하지 않습니다. 대화·Wiki 파일과 다른 슬롯 인증은 보존하지만, 해당 슬롯의 기존 대화 재개 및 관련 미사용 인계 예약은 무효화합니다. 실행 중인 CLI가 있으면 먼저 종료하세요. 브라우저 로그인이나 OpenAI 전체 세션은 로그아웃하지 않습니다. 터미널에서는 `./bin/switcher-helper account logout a` 또는 `b`를 사용합니다.

미등록 계정은 앱 카드의 **계정 연결 · 브라우저 로그인**을 눌러 등록할 수 있습니다. 인증 만료/오류에는 **다시 로그인**을 제공합니다. 브라우저에서 인증하고 필요하면 macOS Keychain 접근을 승인하세요. 로그인 중 진행 상태와 취소 버튼을 표시하며 종료 후 사용량을 갱신합니다. 모델은 호출하지 않습니다. 재빌드 자체는 계정을 삭제하지 않지만 Keychain 접근 승인이 다시 필요하거나 토큰이 만료됐을 수 있습니다. 조회 실패를 미등록으로 간주하지 않습니다. [ADR 0035](docs/adr/0035-menubar-account-login.md)

앱이 상태 서버를 자동 실행하고 연결합니다. 제어 토큰 생성과 빈 loopback 포트 선택도 자동이며 별도 환경 변수 설정이 필요하지 않습니다. 현재 개발 버전은 아래처럼 helper 경로를 실행 인수로 지정하세요.

```sh
cd /Users/bazzi/dev/work/my
go build -o bin/switcher-helper ./cmd/switcher-helper
swift run --package-path macos CodexSwitcher --helper /Users/bazzi/dev/work/my/bin/switcher-helper
```

별도 터미널에서 새 바이너리로 `exec`를 실행합니다. **실제 모델 요청이 발생합니다.** CLI 쪽에는 제어 토큰을 전달할 필요가 없습니다.

```sh
cd /Users/bazzi/dev/work/my
./bin/switcher-helper exec -C . '안녕. 한 문장으로 답해줘.'
```

- 요청 중 해당 세션 카드가 파란 배경으로 표시되고 A/B 계정이 표시되는지 확인합니다. 매우 짧은 요청은 1초 갱신 사이에 완료되어 활성 표시를 놓칠 수 있습니다.
- 정상 완료 후 `실행 종료`, 실패 시 `실행 실패`로 바뀌고 강조가 해제됩니다. 단발 exec 종료는 살아 있는 TUI의 입력 대기가 아닙니다.
- `확인할 세션`에서 다른 실행을 선택하거나 최근 활성 세션 자동 선택을 사용합니다. 프로젝트/worktree/브랜치는 개인정보 보호를 위해 짧은 해시로 표시합니다.
- 앱 종료 시 앱이 시작한 상태 서버만 종료합니다. 실제 CLI/프록시는 별도 수명주기라 계속 동작합니다. 서버가 비정상 종료되면 연결 끊김으로 표시하고 `상태 서버 재시작` 버튼으로 다시 시작합니다. 자동 재실행이나 모델 재전송은 하지 않습니다.
- 강제 종료된 exec의 heartbeat는 5초 뒤 만료됩니다. 예전 실행은 소급 수집하지 않습니다. 자동 계정 전환이나 실패 요청 재전송은 하지 않습니다.
- 앱에는 최근 24시간 상태를 최대 64개 표시하며 살아 있는 실행을 우선 보존합니다. 상태 서버 시작 시와 매시간, heartbeat와 상태 변경 모두 14일을 초과한 관측 파일을 자동 삭제합니다. 실행 중 갱신되는 파일과 Wiki·CLI 대화 기록은 삭제하지 않습니다. 상태 서버가 꺼져 있으면 다음 시작 때 정리합니다.

상태 조회는 1초 polling이며 모델 호출이 아닙니다. API는 loopback 전용으로, [ADR 0032](docs/adr/0032-session-status-control-api.md)에 범위와 보안 규칙을 기록했습니다.

기존 수동 상태 서버는 자동으로 인계하거나 종료하지 않습니다. 디버깅 목적으로 기존 환경 변수 `SWITCHER_CONTROL_TOKEN`/`SWITCHER_CONTROL_URL`에 연결하려면 앱에 `--external-status`를 명시하세요. 이 모드에서는 서버 수명주기를 관리하지 않습니다. 기본 자동 실행 모드는 기존 환경 변수 대신 매번 새 토큰을 사용합니다. [ADR 0034](docs/adr/0034-app-owned-status-server.md)

### 앱 전환 통합 검증

위 명령으로 새 helper를 빌드하고 앱을 재시작하세요. 상태 서버는 자동 시작됩니다. **Wiki 작성과 새 입력 실행은 실제 모델 사용량을 소모합니다.**

1. CLI에서 기억할 단어를 포함한 새 요청을 정상 완료합니다. 앱에서 그 대화를 선택합니다.
2. 반대 계정의 `전환 준비`를 누르고 동일 프로젝트 루트를 선택합니다. `Wiki 작성 시작`을 눌러 저장·백업 완료를 기다립니다. 실제 전송부터 최대 3분이며 자동 재시도는 없습니다.
3. 미해결 작업 없음·Wiki 최신 확인을 체크하고 `인계 준비`를 누릅니다. **사용자 입력 대기** 표시까지 대상 모델 요청은 없어야 합니다.
4. 새 입력에 `기억한 단어는? 그 단어만 답해줘.`를 쓰고 `새 세션 실행`을 누릅니다. 답변과 새 conversation, 메뉴의 대상 계정 표시를 확인합니다. 기존 TUI를 유지하는 동작이 아니라 새로운 단발 exec입니다.
5. 실패 시 `Wiki 저장 재확인`은 후보가 있을 때만 활성화됩니다. 모델 재호출 없이 검사하며 불완전 Wiki나 차단된 원본은 여전히 거절될 수 있습니다. 준비 후 실행 전 `인계 예약 취소`도 확인하세요.
6. 대기 상태에서 앱을 재시작한 뒤 `전환 작업 열기` → `예약 상태 확인`으로 복원합니다. 재시작만으로 모델이 호출되면 안 됩니다. `consumed`는 예약 사용 여부이며 모델 실행 성공을 뜻하지 않습니다.

전환 화면을 닫아도 실행은 이어집니다. 메인 메뉴의 `전환 작업 열기`로 돌아갈 수 있습니다. 앱에는 한 전환 작업을 유지하며 기존 전역 잠금의 동시 exec 제약이 적용됩니다. [ADR 0033](docs/adr/0033-menubar-explicit-handoff-controls.md)

실계정을 사용하지 않는 전체 통합 검사(Go race/vet, helper·앱 빌드, 설치된 CLI 합성 인계, Swift 모델·전환 검사):

```sh
cd /Users/bazzi/dev/work/my
sh macos/Checks/verify.sh
```

Swift 전환 검사만 별도로 실행하려면:

```sh
cd /Users/bazzi/dev/work/my
swiftc -module-cache-path /private/tmp/codex-switcher-clang-cache -emit-library -emit-module -module-name SwitcherUIModel macos/Sources/SwitcherUIModel/*.swift -o /private/tmp/libSwitcherUIModel.dylib -emit-module-path /private/tmp/SwitcherUIModel.swiftmodule
swiftc -module-cache-path /private/tmp/codex-switcher-clang-cache -I /private/tmp -L /private/tmp -lSwitcherUIModel macos/Sources/CodexSwitcher/TransferBridge.swift macos/Sources/CodexSwitcher/TransferStore.swift macos/Checks/TransferCheck.swift -o /private/tmp/codex-switcher-transfer-check
/private/tmp/codex-switcher-transfer-check
```

```sh
swift test --package-path macos
```

`swift test`에는 XCTest를 제공하는 Xcode 도구 체인과 라이선스 동의가 필요합니다. Command Line Tools만 사용하는 환경에서는 다음 합성 모델 검증을 실행할 수 있습니다.

```sh
cd /Users/bazzi/dev/work/my
swiftc -module-cache-path /private/tmp/codex-switcher-clang-cache macos/Sources/SwitcherUIModel/*.swift macos/Checks/ModelCheck.swift -o /private/tmp/codex-switcher-model-check
/private/tmp/codex-switcher-model-check
```

구현 범위와 임시 읽기 전용 연결은 [ADR 0031](docs/adr/0031-menubar-ui-first-increment.md)을 참고하세요.

Go 1.24 이상이 필요합니다. 외부 Go 패키지 의존성은 없습니다. macOS Keychain 빌드에는 Xcode Command Line Tools와 활성화된 CGO(기본값)가 필요합니다.

```sh
go test -race ./...
go vet ./...
go build -o bin/switcher-helper ./cmd/switcher-helper
```

제한된 실행 환경에서 기본 Go 캐시에 쓸 수 없으면 `GOCACHE=/private/tmp/codex-switcher-go-build`를 지정합니다. HTTP 통합 테스트는 loopback 포트를 열 수 있어야 합니다.

합성 데모 실행 (실제 Codex 설정·인증을 변경하지 않음):

```sh
export SWITCHER_CONTROL_TOKEN="local-demo-only-secret"
go run ./cmd/switcher-helper --demo
```

별도 터미널에서:

```sh
curl -N http://127.0.0.1:8765/responses \
  -H 'Content-Type: application/json' \
  -H 'X-Switcher-Demo-Session: demo' \
  -d '{"input":"synthetic"}'

curl http://127.0.0.1:8765/control/status \
  -H 'Authorization: Bearer local-demo-only-secret'
```

데모는 고정된 합성 SSE를 반환하며 실제 모델을 호출하지 않습니다. 데모 헤더를 공식 Codex 세션 식별 방식으로 사용하면 안 됩니다. 실패한 데모 세션은 프로세스 생명주기 동안 차단됩니다.

## 실제 CLI 연결 테스트 (합성 서버)

### 로컬 대화 문맥 전송 확인

```sh
cd /Users/bazzi/dev/work/my
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v ./internal/cliprobe -run '^TestInstalledCodexLocalHistoryPayload$'
```

설치된 CLI와 임시 CODEX_HOME으로 첫 대화 → 같은 ID의 exec resume → 별도 새 대화를 실행합니다. 합성 upstream에 도착한 요청에서 이전 사용자 메시지·assistant 답변의 포함 여부, 새 대화 격리, 정확히 3회 요청을 검사합니다. 원문을 출력하지 않고 임시 기록은 테스트 종료 시 정리합니다. 실제 계정·모델·기존 대화는 사용하지 않습니다.

**PASS는 로컬 기록을 재개 요청에 담는다는 증거일 뿐, A→B 인증 전환이나 동일 TUI 프로세스 유지의 성공을 의미하지 않습니다.** 기존 영속 계정 고정·continuation 격리·Wiki 인계 정책은 변경하지 않았습니다. 실계정 전환 실험은 별도 격리 경로와 서버 참조 안전성 검토가 필요합니다.

설치된 `codex` 실행 파일이 필요합니다. 검증 버전은 **0.153.4**입니다.

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v -timeout 100s ./internal/cliprobe
```

테스트가 자체적으로 임시 프로필과 로컬 서버를 준비하므로 별도 helper 실행은 필요하지 않습니다. 기존 Codex 설정과 로그인 정보를 변경하지 않으며 실제 모델을 호출하지 않습니다. 테스트용 임시 대화 기록은 종료 시 정리합니다.

- 정상 응답과 429·503·스트리밍 중단: CLI와 upstream 각각 요청 1회인지 검사.
- 두 작업 폴더에서 동시 실행: 대화 ID 분리 검사.
- 기존 대화 `exec resume`: ID 유지 검사. 같은 폴더에서 새 대화 시작: 새 ID 검사.
- 동일 TUI 프로세스 내 전환·Wiki 인계·실제 계정 전환은 아직 미검증입니다.

기존 curl 데모에서 오류를 수동 재현하려면 helper를 종료하고 시나리오를 지정해 다시 실행합니다.

```sh
SWITCHER_CONTROL_TOKEN=local-demo-only-secret \
  go run ./cmd/switcher-helper --demo --demo-scenario rate-limit
```

`success`, `rate-limit`, `server-error`, `partial`을 지원합니다. `/control/status`의 `upstream_calls`로 요청 횟수를 확인할 수 있습니다. 실패 후 같은 demo 세션을 다시 호출하면 409로 차단되며 횟수는 증가하지 않습니다.

자세한 검증 범위는 [ADR 0012](docs/adr/0012-installed-cli-synthetic-probe.md)를 참고하세요.

## 브라우저 계정 등록 테스트

아래 로그인 명령은 **실제 브라우저 인증을 시작하고 Switcher 전용 Keychain 항목에 저장**합니다. 기존 `~/.codex` 로그인 자료는 가져오거나 수정하지 않습니다. 모델 요청은 하지 않습니다.

```sh
go build -o bin/switcher-helper ./cmd/switcher-helper
./bin/switcher-helper account login a
```

브라우저에서 로그인한 뒤 `"state":"succeeded"`를 확인하세요. macOS Keychain 승인이 표시되면 helper 경로를 확인하세요. 로그인 취소는 `Ctrl+C`이며 최대 5분 대기합니다. 브라우저가 열리지 않을 경우 이 버전에서는 수동 인증 URL을 출력하지 않습니다.

새 터미널에서도 저장 상태를 확인할 수 있습니다.

```sh
./bin/switcher-helper account status
```

`registered: true`, `state: stored_unverified`는 로컬 저장 성공을 뜻하며 서버 인증 정상 여부나 사용량을 조회한 결과가 아닙니다. 만료된 계정은 `expired`로 표시합니다. 동일 계정으로 재인증하려면:

```sh
./bin/switcher-helper account reauth a
```

두 번째 계정은 `account login b`로 추가합니다. 같은 계정의 중복 등록과 잘못된 계정으로의 재인증은 거절합니다. 자동 계정 전환은 아직 테스트할 수 없습니다.

중복 판단은 사용자 ID와 워크스페이스(account) ID를 함께 비교합니다. 같은 Business 워크스페이스라도 사용자 ID가 다르면 등록을 허용합니다. `account_already_registered`는 두 값이 모두 같은 경우이며, `account_user_identity_unavailable`은 지원하는 사용자 claim이 없어 판단할 수 없는 경우입니다. 기존 A를 삭제하지 않고 B 로그인을 다시 검증할 수 있습니다. 실제 토큰 형식 호환성은 사용자 확인이 필요합니다. [ADR 0026](docs/adr/0026-user-and-workspace-identity.md)

인증 관련 일반 테스트는 합성 데이터만 사용합니다. 네이티브 Keychain 저장을 따로 검사하려면:

```sh
SWITCHER_KEYCHAIN_INTEGRATION=1 go test -count=1 -v -timeout 30s ./internal/credentialstore
```

이 테스트는 고유 이름의 합성 항목만 만들고 종료 시 삭제합니다. 강제 종료 시 임시 인증 파일 잔류 등의 한계는 [ADR 0013](docs/adr/0013-browser-login-and-keychain.md)에 기록했습니다. 오류가 나면 **오류 코드만 공유하고 auth.json·토큰·인증 URL은 공유하지 마세요.**

## 실제 계정으로 인사 요청 1회 테스트

등록된 `a` 계정으로 테스트합니다. **이 명령은 실제 모델 요청을 보내 계정 사용량을 소모합니다.** 별도 helper 실행이나 전역 Codex 설정 변경은 필요하지 않습니다.

```sh
go build -o bin/switcher-helper ./cmd/switcher-helper
./bin/switcher-helper live-test a
```

내부에서 로컬 프록시와 새 CLI를 열어 “짧게 안녕이라고만 답해줘. 도구를 사용하지 마.”를 보냅니다. 답변과 함께 진단 JSON에 `succeeded: true`, `cli_requests: 1`, `proxy_admissions: 1`, `last_http_status: 200`이 나오면 성공입니다. 준비·실행·종료까지 자동으로 처리하며, 기존 대화나 프로젝트 작업을 이어가는 명령은 아닙니다.

모델을 생략하면 설치된 CLI의 기본 모델을 사용합니다. 기존 config의 모델 선택은 공유하지 않으므로 필요하면 `--model`로 계정에서 사용 가능한 모델명을 지정하세요.

`login_credentials_expired`가 나오면 사용자가 직접 `./bin/switcher-helper account reauth a`를 실행하세요. 실패 시 helper는 자동 재전송하지 않습니다. 재시험 명령을 다시 실행하는 것은 별개의 새 모델 요청입니다. 오류 공유 시 진단 JSON과 오류 코드만 보내고 원본 인증 자료는 보내지 마세요.

`proxy_admissions: 0`이면 OpenAI 전송 전 로컬 검사에서 거절된 것입니다. 이 경우 `local_rejection_code`에서 구체적인 사유를 확인할 수 있습니다. CLI 기본 모델이 보내는 인라인 `additional_tools` 정의도 지원하며, 과거 대화 참조와는 구분합니다.

실제 토큰을 사용하지 않는 동일 경로의 합성 테스트:

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v -timeout 100s ./internal/livetest
```

지원 범위와 backend 경로의 호환성 제약은 [ADR 0014](docs/adr/0014-single-account-live-smoke-test.md)에 기록했습니다.

## 계정별 사용량 조회

저장된 인증으로 사용량 메타데이터만 읽습니다. **모델을 호출하지 않으며 대화나 Codex 설정을 변경하지 않습니다.** 저장소 폴더에서 실행하세요.

```sh
cd /Users/bazzi/dev/work/my
go build -o bin/switcher-helper ./cmd/switcher-helper
./bin/switcher-helper usage
```

`a`, `b`를 각각 조회하고 JSON 한 줄로 출력합니다. 특정 계정만 조회하려면 `usage a`를 사용하세요. 1분 간격으로 계속 확인하려면:

```sh
./bin/switcher-helper usage --watch
```

즉시 한 번 조회한 다음 60초마다 갱신합니다. `Ctrl+C`로 종료합니다. `usage --watch a`도 가능합니다. 옵션은 계정명 앞에 둡니다. 수동 갱신은 `usage`를 다시 실행하면 됩니다.

- `usage.primary`, `usage.secondary`: 서버가 알려준 사용률·잔여율·구간 길이(초)·초기화 시각(UTC). 확인되지 않은 값은 `null`입니다.
- `usage.remaining_percent`: 두 구간 잔여율의 최솟값. 한쪽이라도 모르면 `null`입니다. 절대 메시지 개수나 모델별 사용 가능 여부를 의미하지 않습니다.
- `state`: `ok`(두 구간 수치 확인), `unknown`, `limit_reached`(서버 명시), `not_registered`, `auth_expired`, `auth_error`, `rate_limited`, `fetch_error`.
- `last_attempt`, `last_success`, `stale`: 마지막 시도/성공 시각과 오래된 데이터 여부. watch 중 실패하면 이전 성공 수치를 유지하되 `stale: true`로 표시합니다. 수신이 멈춘 경우 소비자는 성공 시각에서 2분이 지나면 오래된 값으로 취급해야 합니다.
- `usage.checkpoint_level`: 잔여율 50%·10% 경계의 **관측값**입니다. 아직 Wiki 작성 이벤트나 자동 전환을 실행하지 않습니다. 이전 수치가 남아 있을 수 있으므로 `stale`과 함께 확인하세요.

401/403 또는 `auth_expired`이면 해당 계정을 `account reauth a` 또는 `account reauth b`로 직접 재인증하세요. 429는 조회 제한이며 계정 한도 소진으로 단정하지 않습니다. 오류 직후 추가 재시도는 없고, watch의 다음 정규 주기만 진행합니다. 단발 조회는 실패 상태를 출력한 뒤 종료 코드 1을 반환합니다(미등록·미확인 수치 자체는 오류 아님).

사용량 경로는 공개 API 계약이 아닌 Codex backend 호환 구현이며, 이번 추가분은 합성 응답으로 검증했습니다. 실제 계정의 응답 호환성은 위 명령으로 확인하세요. macOS Keychain 접근 승인이 표시될 수 있습니다. 토큰·계정 식별자·원본 서버 본문은 출력하지 않습니다. 앱/제어 API 연결과 영속 저장은 후속 단계입니다. [ADR 0015](docs/adr/0015-account-usage-polling.md)

## 내부 계정 선택·세션 고정 검증

사용량 실계정 조회 성공 후, 신규 계정 선택 모듈을 추가했습니다. 신규 세션은 기본 90% 소진 미만 계정 중 잔여율이 높은 계정을 선택하고 기존 세션은 원래 계정을 유지합니다. 사용량 미확인·조회 실패·인증 만료 시 다른 계정으로 우회하지 않습니다.

```sh
go test -race -count=1 ./internal/routing
```

이 명령은 합성 데이터와 로컬 프록시로 검증하며 모델을 호출하지 않습니다. 아직 일반 CLI/상주 helper에 연결하지 않은 내부 모듈입니다. 실제 프록시 continuation 연동과 Wiki 전환 준비는 후속 작업입니다. [ADR 0016](docs/adr/0016-new-session-account-selection.md)

## SQLite 세션 저장 검증

macOS 시스템 SQLite(CGO)를 사용하는 내부 저장소와 `routing.NewPersistent`를 추가했습니다. 세션 계정 고정, 응답 ID 소유권, 종료된 요청 의도의 차단 복구 및 14일 초과 유휴 정리를 검증합니다.

```sh
cd /Users/bazzi/dev/work/my
go test -race -count=1 ./internal/affinity ./internal/routing
```

합성 데이터와 임시 DB만 사용하며 로그인이나 모델 호출은 없습니다. [ADR 0017](docs/adr/0017-sqlite-affinity-storage.md)

선택적 `proxy.NewPersistent`는 요청 전 SQLite 기록, `previous_response_id` 소유권 검사, JSON/SSE 완료 ID 저장 및 실패 차단을 연결합니다. 텍스트와 소유권이 확인된 item 참조·함수 호출·문자열 함수 결과를 지원합니다. 기타 CLI 도구 확장은 아직 차단합니다. 기존 demo/live-test와 일반 CLI 런처에는 아직 적용하지 않았습니다. 실사용 자동 복구 기능은 아닙니다. [ADR 0018](docs/adr/0018-persistent-proxy-lifecycle.md), [ADR 0019](docs/adr/0019-function-and-item-ownership.md)

```sh
go test -race -count=1 ./internal/proxy ./internal/affinity ./internal/routing
```

설치된 CLI의 함수 결과 반환 형식만 따로 검증하려면 아래 명령을 사용합니다. 존재하지 않는 합성 도구를 사용하므로 실제 도구/모델 호출은 없습니다. 영속 프록시 전체 CLI 연결 테스트는 아닙니다.

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 ./internal/cliprobe -run TestInstalledCodexFunctionOutput
```

영속 프록시를 거치는 CLI 합성 함수 왕복 테스트도 추가했습니다. 새 메시지/함수 결과 ID의 소유권을 원자적으로 등록하며, CLI 시작 이벤트로 확인한 세션만 연결합니다. 실제 모델·도구를 호출하지 않는 개발용 검증이며 일반 CLI 런처 기능은 아닙니다. [ADR 0020](docs/adr/0020-cli-persistent-function-probe.md)

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -v ./internal/cliprobe -run TestInstalledCodexPersistentFunction
```

영속 경로는 완전한 `additional_tools` 정의와 이전 완료 응답에서 관찰한 assistant/reasoning 이력도 검사합니다. 이력은 세션별 내용 해시와 item ID 소유권을 확인하며 변경된 내용은 차단합니다. 이 확장의 실제 CLI 왕복 검증은 남아 있고, 현재 로컬 합성 테스트로 검증합니다. [ADR 0021](docs/adr/0021-owned-history-and-tool-definitions.md)

CLI 프로세스별 연결 모듈도 추가했습니다. 실행별 인증, 시작 이벤트, 새 대화/resume 구분과 프로젝트 일치를 확인하며, 위 영속 함수 왕복 테스트가 이 모듈과 라우터를 함께 검증합니다. 일반 CLI 실행 명령은 아직 후속 작업입니다. [ADR 0022](docs/adr/0022-cli-process-session-binding.md)

## 로컬 프로젝트 식별

실사용 런처 준비를 위한 로컬 프로젝트 식별 명령을 추가했습니다. 경로·브랜치 원문 대신 프로젝트/worktree/브랜치 해시를 출력하며 세션 생성이나 모델 호출은 하지 않습니다. Git이 없는 경우 지정한 폴더 자체가 프로젝트 루트입니다. [ADR 0023](docs/adr/0023-local-project-identity.md)

```sh
cd /Users/bazzi/dev/work/my
go build -o bin/switcher-helper ./cmd/switcher-helper
./bin/switcher-helper project .
```

## 읽기 전용 CLI 작업 실행 (실계정)

등록한 계정의 사용량을 조회하고 새 세션을 선택한 계정에 고정하여 사용자 프롬프트를 실행합니다. **실제 모델 호출로 사용량을 소모합니다.** 최초 검증은 도구 없는 짧은 인사로 진행하세요.

```sh
cd /Users/bazzi/dev/work/my
go build -o bin/switcher-helper ./cmd/switcher-helper
./bin/switcher-helper exec -C . '짧게 안녕이라고만 답해줘. 도구를 사용하지 마.'
```

`cli_session_started`의 `slot`은 선택된 a/b 계정입니다. 답변 출력과 종료 코드 0을 확인합니다. 시작 이벤트만으로 모델 성공을 의미하지 않습니다. 필요하면 프롬프트 앞에 `--model MODEL`을 추가합니다. 오류가 나면 오류 코드만 공유하고 인증 자료는 공유하지 마세요.

종료 시 `cli_run_finished` 진단 JSON을 출력합니다. `upstream_attempts: 0`이면 모델 서버 전송 전 실패이며 `local_rejection_code`를 확인합니다. 전송 시도가 있으면 `last_http_status`와 `cli_stage`를 함께 확인하세요. 상태 200만으로 성공을 뜻하지 않습니다. 원본 오류·토큰·대화 내용은 진단에 포함하지 않습니다.

HTTP 200 이후 실패는 `response_failure_code`로 구분합니다. `upstream_response_failed`는 서버 실패 이벤트, `completion_shape_invalid`는 완료 응답 형식 불일치, `response_read_canceled`는 읽기 취소, `completion_store_conflict`는 영속 저장 충돌입니다. `completion_seen`은 완료 이벤트를 봤는지, `completion_committed`는 소유권 저장이 끝났는지를 나타냅니다. 실패 시 이 진단 한 줄만 공유하세요.

형식 오류는 `response_format`(sse/json/other/missing/invalid)과 `completion_rejection_code`도 확인합니다. 예를 들어 `completion_json_invalid`는 JSON 문법 또는 중복 키 오류, `completion_error_present`는 오류 객체 포함, `completion_status_invalid`는 완료 상태 불일치입니다. 원문 응답이나 토큰을 공유할 필요가 없습니다. 이 진단만으로 서버 오류 메시지의 구체적인 원인까지 알 수 있는 것은 아닙니다.

헤더 없는 성공 응답이 SSE 접두사로 시작하면 기존 SSE 검증 경로로 처리하며 `response_format=missing_sse`로 표시합니다. 본문을 수정하거나 실패 요청을 다시 보내지는 않습니다.

대화 메타데이터는 저장 후 파일을 다시 읽어 일치 여부를 확인합니다. `cli_record_write_verification_failed`는 이 검증의 실패이며 정상 완료로 표시하지 않습니다. 기존 재개 불가 기록을 자동으로 해제하지 않습니다.

SSE 이력 소유권은 `response.output_item.done`과 최종 완료 응답에서 함께 수집하고 전체 응답 완료 후 커밋합니다. 마지막 완료 이벤트의 output이 비어 있어도 앞서 검증한 항목을 누락하지 않습니다. 이력 해시 생성 불가는 `completion_history_invalid`로 표시합니다. 이 보완은 같은 계정의 명시적 재개를 위한 것이며 Wiki 기반 새 계정 인계를 대신하지 않습니다.

CLI 재개 시 생략되는 빈 `annotations`와 `logprobs` 배열은 이력 해시에서 정규화합니다. 값이 있는 배열과 원문 변경은 계속 구분합니다. 수정 전 해시나 실패 기록은 자동 변환하지 않으므로 재개 검증에는 새 대화를 사용합니다.

영속 프록시는 서버 EOF 검증과 SQLite 커밋 후 최종 완료 이벤트를 전달합니다. 완료 직후 CLI 연결 종료로 저장 전 차단되는 경합을 보완했습니다. 기존 실패 기록은 자동 해제하지 않으므로 해당 기록 대신 새 대화로 검증해야 합니다.

사용량은 즉시 및 60초마다 갱신합니다. 기존 Codex 설정·로그인은 변경하지 않으며 설정 공유는 아직 없습니다. 현재는 읽기 전용이며 TUI·동시 실행·자동 계정 전환은 지원하지 않습니다. 도구 호출 호환성은 추가 검증 중이며 실패 요청은 재전송하지 않습니다. [ADR 0024](docs/adr/0024-read-only-cli-exec.md)

### 같은 대화 재개

이 버전부터 CLI 대화를 Switcher 전용 Application Support 폴더에 보존합니다. 대화 내용이 로컬에 남으며 아직 자동 삭제 기능은 없습니다. 새 실행의 `cli_session_started`에 표시된 `conversation` 값(32자리)을 사용하세요.

```sh
./bin/switcher-helper exec -C . --resume CONVERSATION_HANDLE '방금 대화 내용을 바탕으로 답해줘. 도구는 사용하지 마.'
```

`CONVERSATION_HANDLE`은 실제 출력된 값으로 교체합니다. 같은 프로젝트·worktree·브랜치와 원래 계정으로만 재개합니다. 정상 완료한 대화만 가능하며 중단·실패·알 수 없는 기록은 차단합니다. 프롬프트 없는 자동 재개, `--last` 추정, resume 시 모델 변경은 지원하지 않습니다. 이전 ephemeral 버전에서 삭제된 대화는 재개할 수 없습니다. [ADR 0025](docs/adr/0025-explicit-cli-resume.md)

실제 모델을 호출하지 않는 실행 경로 검증:

```sh
SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 ./internal/clirun
```

### 현재 대화에서 Wiki 작성

정상 완료한 대화의 Codex에게 Wiki를 작성하도록 명시적으로 요청합니다. **기존 계정의 모델 사용량을 소모합니다.** 프로젝트 루트에서 실행하고 HANDLE을 실제 대화 ID로 바꾸세요.

```sh
./bin/switcher-helper exec -C . --resume HANDLE --checkpoint
```

읽기 전용 CLI가 Markdown을 생성하면 helper가 요청 ID·원본·필수 항목을 검증하고 `.codex-switcher/<worktree>/<branch>/<session>/checkpoint.md`에 저장합니다. 본문은 터미널에 출력하지 않습니다. 실제 전송부터 검증까지 최대 3분이며 자동 재시도는 없습니다. `cli_run_finished`만으로 저장 성공을 판단하지 말고 별도의 `checkpoint_saved` 이벤트를 확인하세요. `switch_ready:false`는 정상입니다. 계정 전환이나 다음 모델 요청은 시작하지 않습니다.

저장 성공 시 전역 경로 `~/.codex/switcher/projects/<project>/<worktree>/<branch>/<session>/snapshots.json`에 최근 3개도 보관하며 `backup_saved:true`를 출력합니다. 이후 CLI 또는 앱에서 명시적으로 인계를 준비할 수 있습니다. [ADR 0027](docs/adr/0027-cli-wiki-draft-publication.md), [ADR 0028](docs/adr/0028-wiki-backups-and-explicit-recheck.md)

생성 종료 시 `checkpoint_candidate`에 표시된 checkpoint ID로 저장 상태를 재확인할 수 있습니다. **모델 호출·사용량 조회·타이머 재시작 없이** 보존된 후보 파일을 한 번 검증하고 로컬/전역에 게시합니다.

```sh
./bin/switcher-helper checkpoint recheck -C . --conversation HANDLE --id CHECKPOINT_ID
```

`checkpoint_rechecked`, `backup_saved:true`가 성공 기준입니다. 취소된 요청, 새 요청으로 대체된 ID, 다른 프로젝트는 거절합니다. 후보가 불완전하면 재확인만으로 내용이 완성되지는 않습니다. 후보는 해당 conversation의 private CLI home 내 `wiki-candidate.md`이며 수정하더라도 메타데이터와 종료 마커를 유지해야 합니다. 재확인은 실패한 대화를 재개 가능으로 바꾸거나 계정 전환을 실행하지 않습니다.

## Product principles

### 명시적 Wiki 계정 인계

먼저 원본 대화의 `--checkpoint`를 실행해 저장 성공을 확인하세요. 원본이 A라면 B로 예약합니다. HANDLE은 원본 대화 ID, CHECKPOINT_ID는 저장 이벤트의 ID입니다. `--confirm-boundary`는 미해결 작업이 없고 Wiki가 최신 상태임을 사용자가 확인하는 옵션입니다.

```sh
./bin/switcher-helper handoff prepare -C . --conversation HANDLE --checkpoint CHECKPOINT_ID --to b --confirm-boundary
```

이 명령은 대상 사용량 메타데이터를 조회하지만 모델을 호출하지 않습니다. `handoff_prepared`의 `handoff_id`를 복사하세요. 원본 작업은 예약 중 보류됩니다. 새 사용자 입력으로만 인계를 소비합니다. **아래 exec는 실제 모델 사용량을 소모합니다.**

```sh
./bin/switcher-helper exec -C . --handoff HANDOFF_ID 'Wiki의 현재 상태를 짧게 설명해줘. 도구는 사용하지 마.'
```

새 conversation과 대상 slot, `succeeded:true`를 확인하세요. 새 CLI에는 이전 대화가 아닌 고정 Wiki 본문과 새 입력만 전달됩니다. 같은 예약은 한 번만 연결할 수 있으며 실행 실패 후 자동 재전송하지 않습니다. `--resume`과 함께 사용하지 마세요. 고정/소비된 checkpoint의 재확인은 차단됩니다.

사용 전 예약을 취소하고 원본 작업으로 돌아가려면 다음을 실행하세요. 모델/사용량 조회 없이 예약만 제거하고 Wiki와 고정 파일은 보존합니다.

```sh
./bin/switcher-helper handoff cancel -C . --conversation HANDLE --id HANDOFF_ID
```

앱에서도 위 인계 흐름을 사용할 수 있습니다. 일반 대화형 TUI를 계속 켜둔 채 전환하는 기능은 아직 없습니다.

영속 Wiki 인계의 저장소·라우터·CLI 시작 이벤트 바인딩을 연결했습니다. 예약 복원, 한 번만 소비, 대상 계정 재검사와 원본 continuation 격리를 합성 테스트로 검증합니다. [ADR 0029](docs/adr/0029-persistent-handoff-binding.md), [ADR 0030](docs/adr/0030-cli-explicit-handoff.md)

```sh
go test -race -count=1 ./internal/affinity ./internal/routing ./internal/clisession
```

- 다음 세션에 계정을 지정하고 사용자 입력을 기다립니다. 같은 CLI에서 새 세션을 여는 연동은 검증 대상입니다.
- 실패한 요청은 같은 계정이나 다른 계정으로 자동 재시도하지 않습니다.
- OAuth 토큰과 인증 정보는 로그, 저장소 및 LLM Wiki에 기록하지 않습니다.
- 프록시와 제어 API는 로컬 loopback에서만 접근할 수 있어야 합니다.
- 현재 Codex가 잔여량 50%·10% 및 수동 전환 시 Wiki를 작성하고 다음 세션의 Codex가 참조합니다.

## Development roadmap

1. Codex 프로토콜과 OAuth 흐름 검증
2. 단일 계정 Responses/SSE 프록시
3. 계정별 사용량과 상태 관리
4. 두 계정 라우팅 및 session affinity
5. macOS 메뉴바 앱
6. LLM Wiki Context Bridge
7. 보안 강화, 코드 서명 및 배포

## Security

이 프로젝트는 인증 토큰을 다룹니다. 실제 계정 정보, OAuth 토큰, 세션 덤프 및 민감한 요청·응답 본문을 이 저장소에 커밋하지 마세요.
