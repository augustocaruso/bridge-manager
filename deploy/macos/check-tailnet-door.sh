#!/usr/bin/env bash
set -euo pipefail

if [[ ${1:-} == --help && $# == 1 ]]; then
  printf '%s\n' 'Usage: CHATTY_CERTIFICATE=/absolute/certificate.pem CHATTY_TAILSCALE_BIN=/absolute/tailscale bash check-tailnet-door.sh' \
    'Exit 0: local checks passed; 1: missing, indeterminate or requires privileges; 2: invalid usage.'
  exit 0
fi
if [[ $# != 0 ]]; then printf '%s\n' 'Unknown argument; see --help.' >&2; exit 2; fi
script_dir=$(cd "$(/usr/bin/dirname "$0")" && pwd)
. "$script_dir/chatty-machine.sh"
label=${CHATTY_LAUNCHD_PREFIX}-door
redirect_label=${CHATTY_LAUNCHD_PREFIX}-tailnet-redirect
anchor=com.apple/chatty-tailnet-redirect
certificate=${CHATTY_CERTIFICATE:-}
tailscale_bin=${CHATTY_TAILSCALE_BIN:-}
[[ $certificate == /* ]] || { printf '%s\n' 'Missing or non-absolute CHATTY_CERTIFICATE.' >&2; exit 2; }
[[ $tailscale_bin == /* && -x $tailscale_bin ]] || { printf '%s\n' 'Missing or non-executable CHATTY_TAILSCALE_BIN (absolute path required).' >&2; exit 2; }
chatty_redirect_owner /Library/LaunchDaemons "$redirect_label" required
status=0
report_launch() {
  local service=$1 result
  if launchctl print "$service" >/dev/null 2>&1; then
    printf '%s: loaded (does not prove health).\n' "$service"
  else
    result=$?
    if [[ $result == 113 ]]; then
      printf '%s: not loaded.\n' "$service"
    else
      printf '%s: indeterminate state (code %s).\n' "$service" "$result"
    fi
    status=1
  fi
}
printf '%s\n' 'Chatty door: read-only local inspection; no token or key displayed.'
uid=$(id -u)
report_launch "gui/${uid}/${label}"
report_launch "system/${redirect_label}"
if info=$(/sbin/pfctl -s info 2>/dev/null); then
  enabled=no
  while IFS= read -r line; do
    if [[ $line == Status:* ]]; then
      printf 'pf: %s\n' "$line"
      [[ $line != 'Status: Enabled'* ]] || enabled=yes
    fi
  done <<<"$info"
  if [[ $enabled != yes ]]; then status=1; fi
else
  printf '%s\n' 'pf: indeterminate; /dev/pf inaccessible to user or query failed (without sudo).'
  status=1
fi
if rules=$(/sbin/pfctl -a "$anchor" -s nat 2>/dev/null); then
  if [[ -n $rules ]]; then
    printf 'Anchor %s (translation rules):\n%s\n' "$anchor" "$rules"
  else
    printf 'Anchor %s: no rules.\n' "$anchor"
    status=1
  fi
else
  rules=''
  printf 'Anchor %s: indeterminate; query requires privileges or failed (without sudo).\n' "$anchor"
  status=1
fi
if main_rules=$(/sbin/pfctl -s nat 2>/dev/null); then
  if [[ $main_rules == *'rdr-anchor "com.apple/*" all'* ]]; then
    printf '%s\n' 'Entry point rdr-anchor com.apple/*: present.'
  else
    printf '%s\n' 'Entry point rdr-anchor com.apple/*: absent.'
    status=1
  fi
else
  printf '%s\n' 'Entry point rdr-anchor com.apple/*: indeterminate (without sudo).'
  status=1
fi
listeners=$(/usr/sbin/lsof -nP -a -iTCP:8448 -sTCP:LISTEN -Fn 2>/dev/null) || {
  result=$?
  [[ $result == 1 ]] || printf '8448 query failed: code %s.\n' "$result"
  listeners=''
}
for family in 4 6; do
  if address=$("$tailscale_bin" ip "-${family}" 2>/dev/null) && [[ -n $address ]]; then
    if [[ $family == 6 ]]; then endpoint="[${address}]:8448"; else endpoint="${address}:8448"; fi
    found=no
    while IFS= read -r line; do
      if [[ $line == "n${endpoint}" ]]; then found=yes; fi
    done <<<"$listeners"
    printf 'IPv%s %s: exact listener on 8448 = %s.\n' "$family" "$address" "$found"
    if [[ $found != yes ]]; then status=1; fi
    # Numeric output avoids service-name and reverse-DNS ambiguity in this readback.
    if [[ $rules == *"to ${address} port = 443 -> ${address} port 8448"* ]]; then
      printf 'IPv%s: 443 -> 8448 rule present for current destination.\n' "$family"
    else
      printf 'IPv%s: exact 443 -> 8448 rule missing or unverifiable.\n' "$family"
      status=1
    fi
  else
    printf 'IPv%s Tailscale: indeterminate.\n' "$family"
    status=1
  fi
done
printf '%s\n' 'Listeners on 443 (unprivileged lsof may omit root processes):'
if /usr/sbin/lsof -nP -a -iTCP:443 -sTCP:LISTEN; then
  :
else
  result=$?
  if [[ $result == 1 ]]; then
    printf '%s\n' 'No listener visible to user; this does NOT prove 443 is free.'
  else
    printf '443 query failed: code %s.\n' "$result"
    status=1
  fi
fi
if [[ -f $certificate && ! -L $certificate && -r $certificate ]]; then
  if expiry=$(/usr/bin/openssl x509 -in "$certificate" -noout -enddate 2>/dev/null); then
    printf 'Certificate expiry: %s\n' "$expiry"
    if ! /usr/bin/openssl x509 -in "$certificate" -noout -checkend 0 >/dev/null 2>&1; then
      printf '%s\n' 'Certificate expired.'
      status=1
    fi
  else
    printf '%s\n' 'Certificate expiry: invalid or unreadable bundle.'
    status=1
  fi
else
  printf '%s\n' 'Certificate expiry: bundle missing, symlink or unreadable.'
  status=1
fi
printf '%s\n' 'This inspection does not prove remote reachability, tailnet ACLs or cutover success.'
exit "$status"
