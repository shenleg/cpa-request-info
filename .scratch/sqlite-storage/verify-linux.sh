#!/usr/bin/env bash
# 在既有 Linux 隔离环境执行本轮验收；由主会话显式调用，不在本机运行。
set -euo pipefail
BASE=/opt/cpa-linux-verify.IZCb2S
WORK="$BASE/sqlite-storage.VJ0h2h"
export HOME="$BASE/home"
export PATH="$BASE/tools/go-1.27.2/go/bin:$BASE/tools/node-24.18.0/node-v24.18.0-linux-x64/bin:/usr/local/bin:/usr/bin:/bin"
export GOPROXY=https://goproxy.cn GOTOOLCHAIN=local GOMAXPROCS=2
export GOFLAGS=-p=2 GOMEMLIMIT=512MiB
export TMPDIR="$WORK/tmp"
export CPA_OFFICIAL_SOURCE="$BASE/source/.build/sources/CLIProxyAPI-v8.0.27"
cd "$WORK/source"
case "${1:-}" in
  prepare)
    git init -q
    git config user.name sqlite-verification
    git config user.email sqlite-verification@localhost
    mkdir -p .build/assets
    cp -a "$BASE/confirmed-client-content.ekqw8X/source/.build/assets/." .build/assets/
    ln -s "$BASE/confirmed-client-content.ekqw8X/source/node_modules" node_modules
    go mod download
    ;;
  format)
    find internal cmd tests -name '*.go' -type f -print0 | xargs -0 gofmt -w
    ;;
  unit)
    go test ./internal/audit ./internal/native -count=1
    go test -tags audittest ./internal/audit -count=1
    ;;
  recheck)
    make build-plugin build-retention-test
    go test ./internal/audit -run 'TestSQLite(ExpiryReleasesDatabase|HotJournalRecovery)$' -count=1
    go test ./tests/system -run 'Test(GapHistoryActiveFault|AmbiguousObjectConfigurationStopsCapture|RestartCleanSession|RetentionRejectsInvalidPersistedSettings|RetentionSQLiteCorruptionIsNotRebuilt|SettingsRejectsInvalidPersistedConfiguration|BudgetLoweredMaintenanceContinues)$' -count=1
    ;;
  full)
    make check
    go test ./... -count=1
    ;;
  presentation)
    make build-plugin build-retention-test
    go test ./tests/system -run 'TestHTTPRequestPurposeProtocols$' -count=5
    make tools-test browser-run
    ;;
  verify)
    make verify
    ;;
  plugin)
    make build-plugin
    ;;
  race)
    go test -race ./internal/audit ./internal/native -count=1
    ;;
  *) printf '%s\n' '用法：verify-linux.sh prepare|format|unit|recheck|full|presentation|verify|plugin|race' >&2; exit 2 ;;
esac
