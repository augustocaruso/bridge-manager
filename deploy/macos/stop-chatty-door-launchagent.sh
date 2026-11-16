#!/usr/bin/env bash
set -euo pipefail

fail() { printf '%s\n' "$*" >&2; exit 1; }
if [[ ${1:-} == --help && $# == 1 ]]; then
  printf '%s\n' 'Usage: stop-chatty-door-launchagent.sh (run as the owning user, without sudo; disables and unloads only the door).'
  exit 0
fi
[[ $# == 0 ]] || fail 'Unknown argument; see --help.'
script_dir=$(cd "$(dirname "$0")" && pwd)
. "$script_dir/chatty-machine.sh"
label=${CHATTY_LAUNCHD_PREFIX}-door
[[ $(id -u) != 0 ]] || fail 'Run as the owning user, without sudo.'
uid=$(id -u)
if launchctl print "gui/${uid}/${label}" >/dev/null 2>&1; then
  launchctl disable "gui/${uid}/${label}"
  launchctl bootout "gui/${uid}/${label}"
else
  result=$?
  [[ $result == 113 ]] || fail "Indeterminate LaunchAgent state (${result})."
  launchctl disable "gui/${uid}/${label}"
fi
if launchctl print "gui/${uid}/${label}" >/dev/null 2>&1; then
  fail 'LaunchAgent still loaded.'
else
  result=$?
  [[ $result == 113 ]] || fail 'Cannot confirm unload.'
fi
printf '%s\n' "${label}: disabled and unloaded; plist, discovery, certificates and logs preserved."
