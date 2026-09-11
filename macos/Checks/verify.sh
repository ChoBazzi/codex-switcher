#!/bin/sh
# Synthetic verification only: no account login or live model calls.
set -eu
cd "$(dirname "$0")/../.."
check_dir=$(mktemp -d /private/tmp/switcher-verification.XXXXXXXX)
trap 'rm -r -- "$check_dir"' EXIT
export GOCACHE=${GOCACHE:-/private/tmp/codex-switcher-go-build}
export GOMODCACHE=${GOMODCACHE:-/private/tmp/codex-switcher-go-mod}
export CLANG_MODULE_CACHE_PATH=${CLANG_MODULE_CACHE_PATH:-/private/tmp/codex-switcher-clang-cache}
export SWIFTPM_MODULECACHE_OVERRIDE=${SWIFTPM_MODULECACHE_OVERRIDE:-/private/tmp/codex-switcher-swift-cache}
if rg -q 'TransferStore|TransferPanel|transfer\.' macos/Sources/CodexSwitcher/CodexSwitcherApp.swift; then
    echo 'FAIL: Wiki UI dependency returned to the account-switching app.'
    exit 1
fi
if rg -q 'UsageBridge\.read|nextRefresh' macos/Sources/CodexSwitcher/CodexSwitcherApp.swift; then
    echo 'FAIL: app must not poll upstream usage independently.'
    exit 1
fi
if rg -q '\.confirmationDialog\(' macos/Sources/CodexSwitcher/CodexSwitcherApp.swift; then
    echo 'FAIL: menu-bar confirmations must remain inline, not nested modal dialogs.'
    exit 1
fi
if rg -q 'ScrollView' macos/Sources/CodexSwitcher/CodexSwitcherApp.swift || ! rg -q 'GridRow' macos/Sources/CodexSwitcher/CodexSwitcherApp.swift; then
    echo 'FAIL: account overview must use the non-scrolling two-column grid.'
    exit 1
fi
go test -race ./...
go vet ./...
go build -o bin/switcher-helper ./cmd/switcher-helper
if command -v codex >/dev/null 2>&1; then
    SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -timeout 60s ./internal/directcli
    SWITCHER_CODEX_INTEGRATION=1 go test -race -count=1 -timeout 90s ./cmd/switcher-helper -run 'TestInstalledProbeParser|TestInstalledProbeTools|TestInstalledProbeCompaction|TestInstalledProbeCheckpoint|TestProbe'
else
    echo 'SKIP: Codex CLI is not installed; installed-CLI synthetic test skipped.'
fi
swift build --disable-sandbox --package-path macos --scratch-path /private/tmp/codex-switcher-swift-build
swiftc -module-cache-path "$CLANG_MODULE_CACHE_PATH" macos/Sources/SwitcherUIModel/AccountLogin.swift macos/Sources/SwitcherUIModel/SessionSnapshot.swift macos/Sources/SwitcherUIModel/UsageSnapshot.swift macos/Checks/ModelCheck.swift -o "$check_dir/model-check"
"$check_dir/model-check"
swiftc -module-cache-path "$CLANG_MODULE_CACHE_PATH" -emit-library -emit-module -module-name SwitcherUIModel macos/Sources/SwitcherUIModel/AccountLogin.swift macos/Sources/SwitcherUIModel/SessionSnapshot.swift macos/Sources/SwitcherUIModel/UsageSnapshot.swift -o "$check_dir/libSwitcherUIModel.dylib" -emit-module-path "$check_dir/SwitcherUIModel.swiftmodule"
swiftc -module-cache-path "$CLANG_MODULE_CACHE_PATH" -I "$check_dir" -L "$check_dir" -lSwitcherUIModel macos/Sources/CodexSwitcher/UsageBridge.swift macos/Sources/CodexSwitcher/SessionBridge.swift macos/Sources/CodexSwitcher/ManagedStatusServer.swift macos/Checks/ManagedServerCheck.swift -o "$check_dir/managed-server-check"
"$check_dir/managed-server-check" "$PWD/bin/switcher-helper"
swiftc -module-cache-path "$CLANG_MODULE_CACHE_PATH" -I "$check_dir" -L "$check_dir" -lSwitcherUIModel macos/Sources/CodexSwitcher/AccountLoginStore.swift macos/Checks/AccountLoginCheck.swift -o "$check_dir/account-login-check"
"$check_dir/account-login-check"
swiftc -module-cache-path "$CLANG_MODULE_CACHE_PATH" -I "$check_dir" -L "$check_dir" -lSwitcherUIModel macos/Sources/CodexSwitcher/DirectProxyStore.swift macos/Checks/DirectProxyCheck.swift -o "$check_dir/direct-proxy-check"
"$check_dir/direct-proxy-check"
swiftc -D MENU_LAYOUT_CHECK -module-cache-path "$CLANG_MODULE_CACHE_PATH" -I "$check_dir" -L "$check_dir" -lSwitcherUIModel macos/Sources/CodexSwitcher/CodexSwitcherApp.swift macos/Sources/CodexSwitcher/AccountLoginStore.swift macos/Sources/CodexSwitcher/DirectProxyStore.swift macos/Sources/CodexSwitcher/UsageBridge.swift macos/Sources/CodexSwitcher/SessionBridge.swift macos/Sources/CodexSwitcher/ManagedStatusServer.swift macos/Checks/MenuLayoutCheck.swift -o "$check_dir/menu-layout-check"
"$check_dir/menu-layout-check" --demo
echo 'PASS: account switching verification complete (synthetic only).'
