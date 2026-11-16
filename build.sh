#!/bin/sh
set -eu
export GOMAXPROCS="${GOMAXPROCS:-2}"
go build -p 2 -ldflags "-X main.Tag=$(git describe --exact-match --tags 2>/dev/null || true) -X main.Commit=$(git rev-parse HEAD) -X 'main.BuildTime=$(date -Iseconds)'" "$@" github.com/beeper/bridge-manager/cmd/bbctl
