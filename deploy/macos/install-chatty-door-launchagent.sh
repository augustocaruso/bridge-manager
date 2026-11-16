#!/usr/bin/env bash
set -euo pipefail

fail() { printf '%s\n' "$*" >&2; exit 1; }
usage() {
  printf '%s\n' 'Usage: install-chatty-door-launchagent.sh --install' \
    '       install-chatty-door-launchagent.sh --render IPv4 IPv6 /absolute/tailscale' \
    'Required environment: CHATTY_RUNTIME_ROOT, CHATTY_HOSTNAME, CHATTY_MATRIX_UPSTREAM, CHATTY_SERVER_NAME,' \
    'CHATTY_TAILNET_IPV6_PREFIX (three lowercase hextets, no trailing colon).' \
    '--install also requires CHATTY_TAILSCALE_BIN; run as the owning user, without sudo.' \
    'Optional: CHATTY_BINARY, CHATTY_CERT_DIR, CHATTY_WELLKNOWN_FILE,' \
    'CHATTY_HTTP_LISTEN_IPV4, CHATTY_HTTP_LISTEN_IPV6, CHATTY_BACKEND_CONFIG.' \
    '--render only prints a plist; no network, filesystem writes or service changes.'
}
if [[ ${1:-} == --help ]]; then
  [[ $# == 1 ]] || fail 'Unexpected arguments.'
  usage
  exit 0
fi
script_dir=$(cd "$(dirname "$0")" && pwd)
. "$script_dir/chatty-machine.sh"
label=${CHATTY_LAUNCHD_PREFIX}-door
xml_escape() {
  local value=$1
  value=${value//&/\&amp;}
  value=${value//</\&lt;}
  value=${value//>/\&gt;}
  value=${value//\"/\&quot;}
  value=${value//\'/\&apos;}
  printf '%s' "$value"
}
absolute_path() {
  [[ $1 == /* && $1 != *$'\n'* && $1 != *$'\r'* ]] || fail "$2 must be an absolute, single-line path."
}
configure() {
  [[ -n ${CHATTY_RUNTIME_ROOT:-} ]] || fail 'Missing CHATTY_RUNTIME_ROOT.'
  [[ -n ${CHATTY_HOSTNAME:-} ]] || fail 'Missing CHATTY_HOSTNAME.'
  [[ -n ${CHATTY_MATRIX_UPSTREAM:-} ]] || fail 'Missing CHATTY_MATRIX_UPSTREAM.'
  local server_name=${CHATTY_SERVER_NAME:-}
  [[ -n ${server_name//[[:space:]]/} ]] || fail 'Missing CHATTY_SERVER_NAME.'
  [[ -n ${CHATTY_TAILNET_IPV6_PREFIX:-} ]] || fail 'Missing CHATTY_TAILNET_IPV6_PREFIX.'
  runtime=$CHATTY_RUNTIME_ROOT
  hostname=$CHATTY_HOSTNAME
  matrix_upstream=$CHATTY_MATRIX_UPSTREAM
  [[ ${#hostname} -le 253 && $hostname =~ ^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+ts\.net$ ]] || fail 'CHATTY_HOSTNAME must be a lowercase .ts.net DNS name.'
  local part
  local remaining=$hostname
  while [[ $remaining == *.* ]]; do
    part=${remaining%%.*}
    [[ ${#part} -le 63 ]] || fail 'CHATTY_HOSTNAME has an overlong DNS label.'
    remaining=${remaining#*.}
  done
  [[ $matrix_upstream =~ ^http://(127\.[0-9]+\.[0-9]+\.[0-9]+|\[::1\]):[0-9]+$ ]] || fail 'CHATTY_MATRIX_UPSTREAM must be an HTTP loopback origin with a port.'
  binary_path=${CHATTY_BINARY:-${runtime}/native/bin/chatty-bridges}
  cert_dir=${CHATTY_CERT_DIR:-${runtime}/secrets/chatty-door}
  wellknown_path=${CHATTY_WELLKNOWN_FILE:-${runtime}/config/chatty-door-wellknown.json}
  absolute_path "$runtime" CHATTY_RUNTIME_ROOT
  absolute_path "$binary_path" CHATTY_BINARY
  absolute_path "$cert_dir" CHATTY_CERT_DIR
  absolute_path "$wellknown_path" CHATTY_WELLKNOWN_FILE
  if [[ -n ${CHATTY_BACKEND_CONFIG:-} ]]; then absolute_path "$CHATTY_BACKEND_CONFIG" CHATTY_BACKEND_CONFIG; fi
  [[ -z ${CHATTY_HTTP_LISTEN_IPV4:-} || $CHATTY_HTTP_LISTEN_IPV4 =~ ^127\.[0-9]+\.[0-9]+\.[0-9]+:[0-9]+$ ]] || fail 'CHATTY_HTTP_LISTEN_IPV4 must be a loopback address and port.'
  [[ -z ${CHATTY_HTTP_LISTEN_IPV6:-} || $CHATTY_HTTP_LISTEN_IPV6 =~ ^\[::1\]:[0-9]+$ ]] || fail 'CHATTY_HTTP_LISTEN_IPV6 must be [::1]:port.'
}
argument() { printf '    <string>%s</string>\n' "$(xml_escape "$1")"; }
render_plist() {
  /bin/bash "${script_dir}/install-tailnet-https-redirect.sh" --render "$1" "$2" "$CHATTY_TAILNET_IPV6_PREFIX" >/dev/null
  absolute_path "$3" CHATTY_TAILSCALE_BIN
  printf '%s\n' '<?xml version="1.0" encoding="UTF-8"?>' \
    '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' \
    '<plist version="1.0"><dict>' \
    "  <key>Label</key><string>${label}</string>" \
    '  <key>ProgramArguments</key><array>'
  argument "$binary_path"
  argument --runtime-root; argument "$runtime"
  argument --launchd-prefix; argument "$CHATTY_LAUNCHD_PREFIX"
  argument --server-name; argument "$CHATTY_SERVER_NAME"
  argument door
  argument --listen; argument "${1}:8448"
  argument --listen; argument "[${2}]:8448"
  argument --hostname; argument "$hostname"
  argument --cert-dir; argument "$cert_dir"
  argument --matrix-upstream; argument "$matrix_upstream"
  argument --wellknown-file; argument "$wellknown_path"
  argument --tailscale-bin; argument "$3"
  if [[ -n ${CHATTY_HTTP_LISTEN_IPV4:-} ]]; then argument --http-listen; argument "$CHATTY_HTTP_LISTEN_IPV4"; fi
  if [[ -n ${CHATTY_HTTP_LISTEN_IPV6:-} ]]; then argument --http-listen; argument "$CHATTY_HTTP_LISTEN_IPV6"; fi
  if [[ -n ${CHATTY_BACKEND_CONFIG:-} ]]; then argument --backend-config; argument "$CHATTY_BACKEND_CONFIG"; fi
  printf '%s\n' '  </array>' \
    "  <key>WorkingDirectory</key><string>$(xml_escape "$runtime")</string>" \
    '  <key>RunAtLoad</key><true/>' '  <key>KeepAlive</key><true/>' \
    '  <key>ThrottleInterval</key><integer>30</integer>' \
    '  <key>ExitTimeOut</key><integer>15</integer>' '  <key>Umask</key><integer>63</integer>' \
    "  <key>StandardOutPath</key><string>$(xml_escape "$runtime")/logs/chatty-door.out.log</string>" \
    "  <key>StandardErrorPath</key><string>$(xml_escape "$runtime")/logs/chatty-door.err.log</string>" \
    '</dict></plist>'
}
case ${1:-} in
  --render) [[ $# == 4 ]] || fail 'Supply IPv4, IPv6 and the absolute Tailscale path.'; configure; render_plist "$2" "$3" "$4"; exit 0 ;;
  --install) [[ $# == 1 ]] || fail 'Unexpected arguments.'; configure ;;
  *) usage >&2; exit 2 ;;
esac
[[ $(id -u) != 0 ]] || fail 'Run as the owning user, without sudo.'
absolute_path "${HOME:?HOME is required}" HOME
plist_path="${HOME}/Library/LaunchAgents/${label}.plist"
[[ -f $binary_path && -x $binary_path && ! -L $binary_path ]] || fail "Install the reviewed binary first: ${binary_path}"
tailscale_bin=${CHATTY_TAILSCALE_BIN:-}
[[ -n $tailscale_bin ]] || fail 'Missing CHATTY_TAILSCALE_BIN.'
absolute_path "$tailscale_bin" CHATTY_TAILSCALE_BIN
[[ -x $tailscale_bin ]] || fail 'CHATTY_TAILSCALE_BIN must be executable.'
ipv4=$("$tailscale_bin" ip -4)
ipv6=$("$tailscale_bin" ip -6)
render_plist "$ipv4" "$ipv6" "$tailscale_bin" >/dev/null
[[ ! -L $plist_path && ( ! -e $plist_path || -f $plist_path ) ]] || fail 'Existing plist is not a regular file.'
for directory in "${runtime}/secrets" "$cert_dir"; do
  [[ ! -L $directory && ( ! -e $directory || ( -d $directory && -O $directory ) ) ]] || fail "Unsafe private directory: $directory"
done
umask 077
mkdir -p "${HOME}/Library/LaunchAgents" "${runtime}/logs" "${runtime}/config" "${runtime}/secrets" "$cert_dir"
chmod 700 "$cert_dir"
"$binary_path" --runtime-root "$runtime" wellknown --base-url "https://${hostname}" --write "$wellknown_path" >/dev/null
printf 'Discovery published at %s; private certificates at %s (0700).\n' "$wellknown_path" "$cert_dir"
uid=$(id -u)
if launchctl print "gui/${uid}/${label}" >/dev/null 2>&1; then
  launchctl bootout "gui/${uid}/${label}"
else
  result=$?
  [[ $result == 113 ]] || fail "Unknown LaunchAgent state (${result})."
fi
render_plist "$ipv4" "$ipv6" "$tailscale_bin" >"$plist_path"
chmod 600 "$plist_path"
plutil -lint "$plist_path" >/dev/null
launchctl enable "gui/${uid}/${label}"
launchctl bootstrap "gui/${uid}" "$plist_path"
launchctl print "gui/${uid}/${label}" >/dev/null
printf '%s\n' "${label}: loaded with KeepAlive; ${ipv4}:8448 and [${ipv6}]:8448 configured." \
  "Plist: ${plist_path}" 'Verify listeners and TLS; a loaded service is not proof of health.'
