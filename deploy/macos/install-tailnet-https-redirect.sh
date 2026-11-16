#!/usr/bin/env bash
set -euo pipefail

export PATH=/usr/bin:/bin:/usr/sbin:/sbin

fail() { printf '%s\n' "$*" >&2; exit 1; }
usage() {
  printf '%s\n' 'Usage: install-tailnet-https-redirect.sh --install IPv4 IPv6 IPv6-prefix' \
    '       install-tailnet-https-redirect.sh --render IPv4 IPv6 IPv6-prefix' \
    '     install-tailnet-https-redirect.sh --render-plist' \
    '--render only validates and prints rules; no root privileges, installation writes or network access.'
}
validate_addresses() {
  local octet second third fourth normalized
  [[ $1 =~ ^100\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ ]] || fail 'IPv4 outside the Tailscale range.'
  second=${BASH_REMATCH[1]}; third=${BASH_REMATCH[2]}; fourth=${BASH_REMATCH[3]}
  for octet in "$second" "$third" "$fourth"; do
    [[ $octet == 0 || $octet != 0* ]] || fail 'Noncanonical IPv4.'
    (( 10#$octet <= 255 )) || fail 'Invalid IPv4.'
  done
  (( 10#$second >= 64 && 10#$second <= 127 )) || fail 'IPv4 outside 100.64.0.0/10.'
  [[ $3 =~ ^fd[0-9a-f]{2}:[0-9a-f]{1,4}:[0-9a-f]{1,4}$ ]] || fail 'Supply the authorized ULA IPv6 prefix as three lowercase hextets, without a trailing colon.'
  [[ $2 == "$3":* ]] || fail 'IPv6 is outside the configured tailnet prefix.'
  if [[ $2 == *::* ]]; then
    normalized=${2/::/:}
    normalized=${normalized%:}
    [[ $normalized =~ ^[0-9a-f]{1,4}(:[0-9a-f]{1,4}){2,6}$ ]] || fail 'Invalid IPv6.'
  else
    [[ $2 =~ ^[0-9a-f]{1,4}(:[0-9a-f]{1,4}){7}$ ]] || fail 'Invalid IPv6.'
  fi
}
render_rules() {
  validate_addresses "$1" "$2" "$3"
  # Destination-only matching deliberately survives utun renumbering; see tailnet-door.md.
  printf 'rdr pass inet proto tcp from any to %s port 443 -> %s port 8448\n' "$1" "$1"
  printf 'rdr pass inet6 proto tcp from any to %s port 443 -> %s port 8448\n' "$2" "$2"
}
check_file() {
  [[ ! -L $1 && ( ! -e $1 || -f $1 ) ]] || fail "Nonregular destination: $1"
  if [[ -e $1 ]]; then
    [[ $(stat -f '%u' "$1") == 0 ]] || fail "File not owned by root: $1"
    [[ $(stat -f '%Lp' "$1") == "$2" ]] || fail "Unexpected permissions: $1"
  fi
}
check_directory() {
  [[ -d $1 && ! -L $1 && $(stat -f '%u' "$1") == 0 ]] || fail "Root directory missing or unsafe: $1"
  local mode
  mode=$(stat -f '%Lp' "$1")
  (( (8#$mode & 8#022) == 0 )) || fail "Directory writable by others: $1"
}
load_anchor() {
  check_directory "$state_dir"
  [[ $(stat -f '%Lp' "$state_dir") == 700 ]] || fail 'State must have mode 0700.'
  check_file "$rules_path" 600
  check_file "$receipt_path" 600
  [[ -f $rules_path ]] || fail 'Rules missing; reinstall with current IPs.'
  local main_rules
  main_rules=$(pfctl -s nat 2>/dev/null) || fail 'Cannot query the main ruleset.'
  [[ $main_rules == *'rdr-anchor "com.apple/*" all'* ]] || fail 'Apple anchor still missing; launchd will retry without changing the main ruleset.'
  pfctl -a "$anchor" -f "$rules_path"
  printf '%s\n' "Rules loaded only in ${anchor}."
  if [[ -e $receipt_path ]]; then
    printf '%s\n' 'Existing receipt preserved; no new token requested.'
  else
    # Keep both streams private: macOS prints the enable token on stderr. See tailnet-door.md.
    pfctl -E >"$receipt_path" 2>&1 || fail 'Failed to enable pf; private receipt preserved for diagnosis.'
  fi
  local line count=0
  while IFS= read -r line || [[ -n $line ]]; do
    if [[ $line =~ ^Token[[:space:]]*:[[:space:]]*([0-9]+)[[:space:]]*$ ]]; then
      count=$((count + 1))
    fi
  done <"$receipt_path"
  [[ $count == 1 ]] || fail 'Receipt without a unique token; coordinator intervention required, without acquiring another reference.'
  local info
  info=$(pfctl -s info 2>/dev/null) || fail 'Cannot confirm pf state.'
  [[ $info == 'Status: Enabled'* ]] || fail 'pf is not enabled; receipt preserved, without acquiring another reference.'
  printf '%s\n' 'pf enabled; root-only receipt preserved, without displaying token.'
}

render_daemon_plist() {
  cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>${label}</string>
  <key>EnvironmentVariables</key><dict><key>CHATTY_LAUNCHD_PREFIX</key><string>${prefix}</string></dict>
  <key>ProgramArguments</key><array><string>/bin/bash</string><string>${helper_path}</string><string>--load</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>30</integer>
  <key>Umask</key><integer>63</integer>
  <key>StandardOutPath</key><string>/var/log/${label}.out.log</string>
  <key>StandardErrorPath</key><string>/var/log/${label}.err.log</string>
</dict></plist>
EOF
}

case ${1:-} in
  --help) [[ $# == 1 ]] || fail 'Extra arguments.'; usage; exit 0 ;;
  --render) [[ $# == 4 ]] || fail 'Supply IPv4, IPv6 and the authorized IPv6 prefix.'; render_rules "$2" "$3" "$4"; exit 0 ;;
  --render-plist) [[ $# == 1 ]] || fail 'Extra arguments.' ;;
  --load) [[ $# == 1 ]] || fail 'Extra arguments.' ;;
  --install) [[ $# == 4 ]] || fail 'Supply IPv4, IPv6 and the authorized IPv6 prefix.'; validate_addresses "$2" "$3" "$4" ;;
  *) usage >&2; exit 2 ;;
esac
# Never infer a user's installation from root's HOME or sudo's stripped environment.
# See tailnet-door.md. Boot uses only the prefix frozen in the root-owned plist.
if [[ $1 == --install ]]; then
  [[ -n ${CHATTY_MACHINE_CONFIG:-}${CHATTY_LAUNCHD_PREFIX:-} ]] || fail 'Select CHATTY_MACHINE_CONFIG or CHATTY_LAUNCHD_PREFIX explicitly through sudo env.'
fi
if [[ $1 == --load ]]; then
  [[ -n ${CHATTY_LAUNCHD_PREFIX:-} ]] || fail 'Boot loader requires an explicit CHATTY_LAUNCHD_PREFIX in its root-owned plist.'
else
  script_dir=$(cd "$(dirname "$0")" && pwd)
  . "$script_dir/chatty-machine.sh"
fi
prefix=$CHATTY_LAUNCHD_PREFIX
[[ $prefix =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]*$ ]] || fail 'Invalid CHATTY_LAUNCHD_PREFIX.'
label=${prefix}-tailnet-redirect
anchor=com.apple/chatty-tailnet-redirect
rules_path=/etc/pf.anchors/${label}
helper_path=/Library/PrivilegedHelperTools/${label}.sh
plist_path=/Library/LaunchDaemons/${label}.plist
state_dir=/var/run/${label}
receipt_path="${state_dir}/pf-enable.receipt"
if [[ $1 == --render-plist ]]; then render_daemon_plist; exit 0; fi
[[ $(id -u) == 0 ]] || fail 'This operation requires root; --render does not.'
if [[ $1 == --install ]]; then
  chatty_redirect_owner /Library/LaunchDaemons "$label" optional
  interfaces=$(ifconfig -a) || fail 'Cannot query local IPs.'
  found_ipv4=no
  found_ipv6=no
  while read -r kind address _; do
    if [[ $kind == inet && $address == "$2" ]]; then found_ipv4=yes; fi
    if [[ $kind == inet6 && $address == "$3" ]]; then found_ipv6=yes; fi
  done <<<"$interfaces"
  [[ $found_ipv4 == yes && $found_ipv6 == yes ]] || fail 'Both IPs must be assigned locally; use the exact output of tailscale ip -4/-6.'
fi
umask 077
if [[ ! -e /Library/PrivilegedHelperTools && ! -L /Library/PrivilegedHelperTools ]]; then
  [[ $1 == --install ]] || fail 'Loader directory missing; reinstall.'
  check_directory /Library
  install -d -o root -g wheel -m 755 /Library/PrivilegedHelperTools
fi
check_directory /Library/PrivilegedHelperTools
check_directory /Library/LaunchDaemons
check_directory /etc/pf.anchors
for target in "$helper_path" "$plist_path" "$rules_path"; do
  case $target in "$helper_path") check_file "$target" 700 ;; *) check_file "$target" 600 ;; esac
done
if [[ -e $state_dir || -L $state_dir ]]; then
  check_directory "$state_dir"
  [[ $(stat -f '%Lp' "$state_dir") == 700 ]] || fail 'State must have mode 0700.'
else
  mkdir -m 700 "$state_dir"
fi
if [[ $1 == --load ]]; then load_anchor; exit 0; fi

# Stop the one-shot loader before replacing its root-owned inputs; keep its enable reference.
if launchctl print "system/${label}" >/dev/null 2>&1; then
  launchctl bootout "system/${label}"
else
  result=$?
  [[ $result == 113 ]] || fail "Indeterminate LaunchDaemon state (${result})."
fi
check_file "$receipt_path" 600
render_rules "$2" "$3" "$4" >"$rules_path"
install -o root -g wheel -m 700 "$0" "$helper_path"
printf '%s\n' "Rules written to ${rules_path}; loader installed at ${helper_path}."
render_daemon_plist >"$plist_path"
plutil -lint "$plist_path" >/dev/null
launchctl enable "system/${label}"
launchctl bootstrap system "$plist_path"
launchctl print "system/${label}" >/dev/null
printf '%s\n' "${label}: loaded. Confirm rules and status with check-tailnet-door.sh; bootstrap does not prove loader success."
