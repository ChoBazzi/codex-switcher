#!/bin/sh
# Optional legacy Wiki harness checks. No real credentials or models.
set -eu
cd "$(dirname "$0")/../.."
export GOCACHE=${GOCACHE:-/private/tmp/codex-switcher-go-build}
export GOMODCACHE=${GOMODCACHE:-/private/tmp/codex-switcher-go-mod}
check_dir=$(mktemp -d /private/tmp/switcher-wiki-check.XXXXXXXX)
# Only the exact directory created above belongs to this check.
trap 'rm -r -- "$check_dir"' EXIT
go test -race ./internal/checkpoint ./internal/wikidraft ./internal/handoff ./internal/affinity
if command -v codex >/dev/null 2>&1; then
    SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -timeout 180s ./internal/clirun
fi
swiftc -module-cache-path /private/tmp/codex-switcher-clang-cache -emit-library -emit-module -module-name SwitcherUIModel macos/Sources/SwitcherUIModel/*.swift -o "$check_dir/libSwitcherUIModel.dylib" -emit-module-path "$check_dir/SwitcherUIModel.swiftmodule"
swiftc -module-cache-path /private/tmp/codex-switcher-clang-cache -I "$check_dir" -L "$check_dir" -lSwitcherUIModel macos/Sources/CodexSwitcher/TransferBridge.swift macos/Sources/CodexSwitcher/TransferStore.swift macos/Checks/TransferCheck.swift -o "$check_dir/transfer-check"
"$check_dir/transfer-check"
echo 'PASS: optional Wiki harness checks (synthetic only).'
