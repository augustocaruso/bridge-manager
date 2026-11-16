#!/bin/sh
set -eu
if [ "$#" -ne 0 ]; then
    printf '%s\n' 'Usage: sh cmd/chatty-bridges/build.sh (no arguments; output exclusively in staging)' >&2
    exit 2
fi
export GOMAXPROCS="${GOMAXPROCS:-2}"
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
. "$script_dir/../../deploy/macos/chatty-machine.sh"
root="${CHATTY_STAGING_ROOT-${HOME}/Library/Caches/Chatty/bridge-manager}"
if [ -z "$root" ]; then
    printf '%s\n' 'CHATTY_STAGING_ROOT is set but empty; unset it or set an absolute path.' >&2
    exit 1
fi
mkdir -p "$root"
output="$(mktemp -d "$root/cli-XXXXXXXX")/chatty-bridges"
go build -p 2 -trimpath -o "$output" github.com/beeper/bridge-manager/cmd/chatty-bridges
printf '%s\n' "$output"
