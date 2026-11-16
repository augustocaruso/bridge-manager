# Chatty Tailnet Door On macOS

These scripts configure the private HTTPS door and its optional TCP 443-to-8448
redirect. They do not build/copy the CLI, configure Matrix/bridges, log in,
pair devices or authorize production changes. Perform installation/cutover only
with an approved binary, an exclusive operator window, backups and a verified
rollback. Never run a development checkout as a production service.

The door runs as the current user, bound only to explicitly selected Tailscale
IPs. The root redirect matches those exact destinations and preserves the IP.
It never enables IP forwarding, edits `/etc/pf.conf`, redirects other destinations
or manages bridge/PostgreSQL services.

## Required Configuration

There are no compiled machine/user addresses or absolute home paths. Configure
the following in the per-user machine file documented in [README.md](../../README.md)
or the operator's environment before rendering/installing. Environment values
override file values; positional render arguments override those settings.
The same optional `$HOME/Library/Application Support/Chatty/machine.json` is
read by every CLI/build/deployment invocation, including noninteractive launchd
commands. Use `CHATTY_MACHINE_CONFIG` for an explicit alternate file.

| Variable | Meaning |
| --- | --- |
| `CHATTY_LAUNCHD_PREFIX` | `local.chatty`; one prefix for all labels and redirect-owned filesystem paths. |
| `CHATTY_RUNTIME_ROOT` | Absolute installation runtime containing native/bin, bridges, config and secrets. |
| `CHATTY_HOSTNAME` | Authorized lowercase Tailscale DNS name, e.g. `node.example.ts.net`. |
| `CHATTY_MATRIX_UPSTREAM` | HTTP loopback Matrix origin with port; e.g. `http://127.0.0.1:8009` if the door owns HTTP 8008. |
| `CHATTY_SERVER_NAME` | Actual Matrix `server_name`, e.g. `matrix.example.org`; no default and not the Tailscale hostname. |
| `CHATTY_TAILNET_IPV6_PREFIX` | Authorized tailnet ULA prefix, exactly three lowercase hextets with no trailing colon. Obtain it from the network configuration, not from these examples. |
| `CHATTY_TAILSCALE_BIN` | Absolute executable path for installation/checks; render takes it as a positional argument instead. |

Optional door installer settings:

| Variable | Default / meaning |
| --- | --- |
| `CHATTY_BINARY` | `$CHATTY_RUNTIME_ROOT/native/bin/chatty-bridges`, already installed reviewed executable. |
| `CHATTY_CERT_DIR` | `$CHATTY_RUNTIME_ROOT/secrets/chatty-door`, private directory 0700. |
| `CHATTY_WELLKNOWN_FILE` | `$CHATTY_RUNTIME_ROOT/config/chatty-door-wellknown.json`, dedicated publication file. |
| `CHATTY_HTTP_LISTEN_IPV4` | No default; e.g. `127.0.0.1:8008` for local HTTP discovery/proxy. |
| `CHATTY_HTTP_LISTEN_IPV6` | No default; e.g. `[::1]:8008`. |
| `CHATTY_BACKEND_CONFIG` | No default; absolute private health JSON path. |

The Chatty installer supplies these installation-specific values and the two
local IPs; scripts never use a developer's machine as a default. The user
LaunchAgent directory is resolved from `$HOME` and its domain uses `id -u`,
not a named account. Do not run user-agent installation or stop as root.
A Tailscale CLI/wrapper must work without a terminal under launchd; see the
backend configuration guidance in [CHATTY.md](../../CHATTY.md).

The generated plist passes global `--runtime-root`, `--launchd-prefix` and `--server-name` before
`door` and explicit hostname/listeners/cert-dir/upstream/wellknown-file/tailscale-bin
flags. Optional
HTTP/backend variables are emitted as the corresponding flags. When regenerating
an existing installation, fill them all from its approved configuration: omitting
optional flags disables those features. Rendering does not read a live plist.
Before upgrading an existing door with health enabled, add
`--server-name <actual-Matrix-server_name>` before `door` in its argument vector.
The health snapshot reports this identity; startup refuses an omitted value.

## Render Without Installing

Synthetic addresses below are examples only, not a routable installation:

```sh
export CHATTY_RUNTIME_ROOT="$HOME/.local/share/chatty/runtime"
export CHATTY_HOSTNAME=node.example.ts.net
export CHATTY_MATRIX_UPSTREAM=http://127.0.0.1:8009
export CHATTY_SERVER_NAME=matrix.example.org
export CHATTY_TAILNET_IPV6_PREFIX=fd00:1234:5678
export CHATTY_HTTP_LISTEN_IPV4=127.0.0.1:8008
export CHATTY_HTTP_LISTEN_IPV6='[::1]:8008'
export CHATTY_BACKEND_CONFIG="$CHATTY_RUNTIME_ROOT/config/backend.json"

bash deploy/macos/install-chatty-door-launchagent.sh --render \
  100.64.0.10 fd00:1234:5678::10 /absolute/path/to/tailscale | plutil -lint -
bash deploy/macos/install-tailnet-https-redirect.sh --render \
  100.64.0.10 fd00:1234:5678::10 "$CHATTY_TAILNET_IPV6_PREFIX"
bash deploy/macos/install-tailnet-https-redirect.sh --render-plist | plutil -lint -
```

Missing configuration or malformed addresses fail clearly before changes.
IPv4 must be canonical and inside 100.64.0.0/10. IPv6 must match the explicitly
configured ULA prefix and use the supported lowercase literal form. No zones,
newlines, shell injection or XML injection are accepted as network values;
paths in plist fields are XML-escaped. `--render` does not create directories,
query Tailscale or launchctl, issue certificates or change pf. All scripts offer
`--help`. The door installer requires explicit `--install`, not a bare invocation.

## Supervised Installation And Cutover

1. Record the approved candidate hash, disk/uptime, current public/local endpoints, existing door arguments and Tailscale Serve configuration in private evidence. Back up the installed binary, relevant plists and configs. Do not print tokens/keys or copy them into public reports.
2. Install the reviewed binary separately. Configure the variables above for this installation, including optional HTTP and health fields already in use. Ensure the selected upstream is available and distinct from the HTTP listeners. Never change Matrix server_name, database or bridge registrations merely to reroute HTTP.
3. As the owning GUI user, set `CHATTY_TAILSCALE_BIN` to the absolute working CLI and run `bash deploy/macos/install-chatty-door-launchagent.sh --install`. It obtains both local IPs with `ip -4/-6`, validates the configured prefix, restricts certificates, publishes discovery with the selected origin/runtime and replaces only the door's user plist. It unloads/reloads only that label, with readback. Unknown state aborts.
4. Before changing public 443, test valid TLS, Matrix versions and discovery on 8448 from another tailnet machine, over both families. Anonymous provisioning whoami should reach authentication, not 404; this is not proof of authenticated provisioning. Never use curl -k.
5. If replacing Tailscale Serve, save its exact rollback command/config and clear only the approved mapping. Privileged listener inspection must prove 443 has no conflicting listener. Unprivileged lsof output cannot prove root-owned ports are free.
6. Capture both IPs as the user, then pass literal arguments to root: `sudo env CHATTY_MACHINE_CONFIG="/absolute/path/to/machine.json" /bin/bash deploy/macos/install-tailnet-https-redirect.sh --install "$ipv4" "$ipv6" "$CHATTY_TAILNET_IPV6_PREFIX"`. Select the same owning user's machine file for privileged removal. Both operations refuse an omitted explicit file/prefix instead of consulting root's defaults. Alternatively pass the selected `CHATTY_LAUNCHD_PREFIX` explicitly through `sudo env`. The installer checks that both addresses are local and does not run Tailscale as root. The ULA network prefix is a positional argument, separate from the launchd label prefix.
7. For read-only checks, set `CHATTY_CERTIFICATE="$CHATTY_CERT_DIR/certificate.pem"` (or the actual configured cert path) and `CHATTY_TAILSCALE_BIN`, then run `bash deploy/macos/check-tailnet-door.sh`. Missing variables fail before service queries. It inspects only the current user's door, the redirect, listeners, numeric rules and public certificate dates; never receipt/private-key contents. Lack of pf privilege is unknown, not healthy. Privileged operator checks are `pfctl -s info`, `pfctl -s nat` and `pfctl -a com.apple/chatty-tailnet-redirect -s nat`; do not use -s all or -s References in shared output.
8. Verify public 443 TLS/discovery/Matrix/provisioning over IPv4 and IPv6 from another machine, existing clients without re-login, and optional local HTTP/health. After a supervised reboot/login, verify again. A loaded launchd job or successful bootstrap is not readiness. Record redacted results and candidate identity; do not claim GUI acceptance from unit tests.

A GUI LaunchAgent starts only with its user session. PF may be active before the
door. IPs are pinned at installation; changing node/account/IP requires updating
both agent and redirect. Discovery is a snapshot and must be republished after
bridge inventory changes. No automatic address migration or watcher exists.

## Installed Objects And Compatibility

All labels are derived from `CHATTY_LAUNCHD_PREFIX`, default `local.chatty`:
`<prefix>-door` and `<prefix>-tailnet-redirect`. Before an upgrade, record the
existing prefix in the machine file to keep managing the same installation.
Changing the default does not rename, unload or migrate services. The kernel
anchor remains `com.apple/chatty-tailnet-redirect` so existing PF dispatch and
enable receipts keep working. This single redirect anchor is not a namespace
for concurrent installations, even with different launchd prefixes. Installation,
removal and inspection reject any competing `*-tailnet-redirect.plist` in
`/Library/LaunchDaemons` before querying or changing PF. Removal and inspection
also require the selected daemon plist; an absent selected installation is not
permission to flush or credit the singleton anchor. To change prefixes, remove
the original redirect first using its original machine file. These checks do
not authorize modifying an existing installation.

| Object | Path |
| --- | --- |
| User door plist | `$HOME/Library/LaunchAgents/<prefix>-door.plist`. |
| Private cert/key bundle | `<cert-dir>/certificate.pem` (0600, directory 0700); never serve this file. |
| Door logs | `<runtime-root>/logs/chatty-door.{out,err}.log`. |
| Redirect LaunchDaemon | `/Library/LaunchDaemons/<prefix>-tailnet-redirect.plist` (root:wheel 0600). |
| Root helper | `/Library/PrivilegedHelperTools/<prefix>-tailnet-redirect.sh` (0700). |
| PF rules | `/etc/pf.anchors/<prefix>-tailnet-redirect` (0600). |
| Boot state | `/var/run/<prefix>-tailnet-redirect` (0700), with private `pf-enable.receipt` (0600). |
| Loader logs | `/var/log/<prefix>-tailnet-redirect.{out,err}.log`, outside user-writable runtime. |

The daemon executes its root-owned helper with /bin/bash --load, never code in
the user's checkout. Its root-owned plist freezes `CHATTY_LAUNCHD_PREFIX` in
EnvironmentVariables. `--load` never reads the user's machine file or the shared
checkout helper; changing the file cannot redirect root boot-time inputs.
Reinstall explicitly to change that frozen configuration. Existing unsafe/symlink root files/directories fail closed.
The helper loads only its child anchor. Main rules must already dispatch
`rdr-anchor "com.apple/*" all`; missing dispatcher fails and launchd retries at
30-second throttle with KeepAlive.SuccessfulExit=false. Launchd has no guaranteed
dependency order, so RunAtLoad/name ordering is not treated as proof of readiness.
The helper never reloads /etc/pf.conf and is not a continuous PF monitor.

Rules match only the selected destination, not an unstable utun interface:

```text
rdr pass inet proto tcp from any to 100.64.0.10 port 443 -> 100.64.0.10 port 8448
rdr pass inet6 proto tcp from any to fd00:1234:5678::10 port 443 -> fd00:1234:5678::10 port 8448
```

`rdr pass` deliberately bypasses a separate filter rule for matched packets;
tailnet ACLs remain required. Disable by removing this redirect, never by adding
a global pass rule. Parsing/local-origin tests do not prove external IPv4/IPv6
ingress; that requires the supervised cutover. See macOS pf.conf(5) translation,
anchors and grammar, pfctl(8) -E/-X, and launchd.plist(5) dependencies.

## Failure, Idempotence And Rollback

Door reinstall republishes only its discovery and replaces/restarts only its
label. `stop-chatty-door-launchagent.sh` disables/unloads that label for the
current user with readback, preserving plist, discovery, certificates and logs.
Never run it under another user's session to stop their service.

Redirect reinstall preserves its current-boot private enable receipt. `pfctl -E`
increments a kernel reference that survives pfctl exit; only `-X <own-token>`
releases it. Both output streams are captured privately because macOS can print
the token on stderr. An existing receipt must contain exactly one token; invalid
receipt is an operator problem, not permission to acquire another reference.
The loader verifies PF enabled after loading. Receipt/kernel references do not
survive reboot. Acquisition/persistence and release/receipt deletion are not
atomic: interruption requires private inspection, never invented/reused tokens.
Do not print the receipt or use bash -x in shareable logs.

Removal (`sudo env CHATTY_MACHINE_CONFIG="/absolute/path/to/machine.json" /bin/bash deploy/macos/remove-tailnet-https-redirect.sh`) unloads
only the daemon, confirms absence, flushes only that anchor's nat rules, confirms
empty translation rules, releases only the receipt's unique token if present,
and removes only its installation files. Failures preserve diagnostic state and
return errors. It never calls PF -d, -F all or flushes global connection state;
existing translated connections may persist until closing/expiry. Other rules,
references and logs are preserved.

Rollback restores the **recorded** prior routing configuration and backed-up
binary/plists/configs, not an assumed developer baseline. Stop the door and remove
only this redirect before restoring a mutually exclusive Tailscale Serve mapping.
If local listener ownership changed, restore that service's approved port config
and plist before starting the prior owner. Require successful unload/readback,
then verify Matrix, discovery and existing clients on both transport families.
Do not restore bridge databases or re-login accounts to conceal failure.

## Offline Checks

```sh
for script in deploy/macos/*.sh; do sh -n "$script" && bash -n "$script" || exit; done
shellcheck deploy/macos/*.sh
GOMAXPROCS=2 nice -n 10 go test -p 2 ./internal/chatty
```

Go tests exercise the actual render scripts, required-value refusal and plist
fields using synthetic values without installing/querying live services. Unit
and rendering checks do not validate root installation, PF ingress, boot,
certificate issuance/renewal, rollback or a running user's GUI.
