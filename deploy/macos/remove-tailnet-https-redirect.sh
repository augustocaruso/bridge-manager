#!/usr/bin/env bash
set -euo pipefail

export PATH=/usr/bin:/bin:/usr/sbin:/sbin
fail() { printf '%s\n' "$*" >&2; exit 1; }
if [[ ${1:-} == --help && $# == 1 ]]; then
  printf '%s\n' 'Usage: sudo env CHATTY_MACHINE_CONFIG=/absolute/machine.json /bin/bash remove-tailnet-https-redirect.sh' \
    'Requires explicit CHATTY_MACHINE_CONFIG or CHATTY_LAUNCHD_PREFIX; removes only the selected Chatty redirect.'
  exit 0
fi
[[ $# == 0 ]] || fail 'Unknown argument; see --help.'
[[ -n ${CHATTY_MACHINE_CONFIG:-}${CHATTY_LAUNCHD_PREFIX:-} ]] || fail 'Select CHATTY_MACHINE_CONFIG or CHATTY_LAUNCHD_PREFIX explicitly through sudo env.'
script_dir=$(cd "$(dirname "$0")" && pwd)
. "$script_dir/chatty-machine.sh"
label=${CHATTY_LAUNCHD_PREFIX}-tailnet-redirect
anchor=com.apple/chatty-tailnet-redirect
rules_path=/etc/pf.anchors/${label}
helper_path=/Library/PrivilegedHelperTools/${label}.sh
plist_path=/Library/LaunchDaemons/${label}.plist
state_dir=/var/run/${label}
receipt_path="${state_dir}/pf-enable.receipt"
[[ $(id -u) == 0 ]] || fail 'Removal requires root.'
chatty_redirect_owner /Library/LaunchDaemons "$label" required
for target in "$state_dir" "$receipt_path" "$rules_path" "$helper_path" "$plist_path"; do
  [[ ! -L $target ]] || fail "Symlink refused: $target"
  if [[ -e $target ]]; then
    [[ $(stat -f '%u' "$target") == 0 ]] || fail "Destination not owned by root: $target"
    if [[ $target == "$state_dir" ]]; then
      [[ -d $target && $(stat -f '%Lp' "$target") == 700 ]] || fail 'State must be a 0700 directory.'
    else
      [[ -f $target ]] || fail "Nonregular file: $target"
      mode=$(stat -f '%Lp' "$target")
      (( (8#$mode & 8#077) == 0 )) || fail "File accessible by others: $target"
    fi
  fi
done
if launchctl print "system/${label}" >/dev/null 2>&1; then
  launchctl bootout "system/${label}"
  printf '%s\n' "${label}: unloaded."
else
  result=$?
  [[ $result == 113 ]] || fail "Indeterminate LaunchDaemon state (${result})."
  printf '%s\n' "${label}: already unloaded."
fi
if launchctl print "system/${label}" >/dev/null 2>&1; then
  fail 'LaunchDaemon still loaded; removal stopped.'
else
  result=$?
  [[ $result == 113 ]] || fail 'Cannot confirm unload.'
fi
# Flush translation rules only: -F all can also affect state/statistic classes. See tailnet-door.md.
pfctl -a "$anchor" -F nat
remaining=$(pfctl -a "$anchor" -s nat 2>/dev/null) || fail 'Cannot confirm anchor cleanup.'
[[ -z $remaining ]] || fail 'Anchor still contains rules; files and reference preserved.'
printf '%s\n' "Only translation rules in ${anchor} removed; global states preserved."
if [[ -e $receipt_path ]]; then
  token=''
  count=0
  while IFS= read -r line || [[ -n $line ]]; do
    if [[ $line =~ ^Token[[:space:]]*:[[:space:]]*([0-9]+)[[:space:]]*$ ]]; then
      token=${BASH_REMATCH[1]}
      count=$((count + 1))
    fi
  done <"$receipt_path"
  [[ $count == 1 ]] || fail 'Receipt without a unique token; preserved for the coordinator, without releasing another reference.'
  pfctl -X "$token" >/dev/null 2>&1 || fail 'Cannot release own reference; receipt preserved.'
  unset token
  rm "$receipt_path"
  printf '%s\n' 'Only this redirect pf reference released; receipt removed.'
else
  printf '%s\n' 'No receipt from this boot; no pf reference released.'
fi
for target in "$plist_path" "$helper_path" "$rules_path"; do
  if [[ -e $target ]]; then
    rm "$target"
    printf 'Removed: %s\n' "$target"
  fi
done
if [[ -d $state_dir ]]; then rmdir "$state_dir"; fi
printf '%s\n' 'Removal complete. pf.conf, Apple anchors, logs and other services preserved; pf not disabled globally.'
