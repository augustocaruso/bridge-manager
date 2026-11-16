# Chatty Bridges: Configuration And Contracts

This Apache-2.0 fork composes the upstream urfave/cli entrypoint and bridge
configuration templates with native Tuwunel, launchctl and filesystem adapters.
It supports Instagram and WhatsApp. It does not use Beeper authentication,
hungryserv, appservice websockets or a new bridge supervisor. The persistent
`door` process is the explicitly supported HTTPS/optional loopback HTTP proxy,
not an installer or lifecycle owner for other services.

## Installation Configuration

All deployment-specific values come from global command-line flags, command
flags, the per-user machine file, environment or existing installation files. See [README.md](README.md) for the global
flag table and examples. Global flags precede the command. Go resolves the
current home directory at startup, never a compiled absolute user path.

| Value | Configuration owner |
| --- | --- |
| Matrix administrative endpoint | `--homeserver` (HTTP literal loopback); separate from `door --matrix-upstream`. |
| Matrix identity domain | `--server-name`; no default. Required for config/register/wipe/health and for `door --backend-config`. |
| Runtime, native configs, registrations, binaries and secrets | `--runtime-root`; default `$HOME/Library/Application Support/Chatty/runtime`. |
| External server source checkout | `--server-repo`; default `$HOME/Library/Application Support/Chatty/matrix-server`. |
| Build output and queue | `--staging-root` defaults to `$HOME/Library/Caches/Chatty/bridge-manager`; `--build-queue` has no default and is required before build effects. |
| Service namespace | `--launchd-prefix`, default `local.chatty`; every label is formed as `<prefix>-<component>`. |
| Public discovery origin | Global `--public-base-url`; no default. `wellknown --base-url` overrides it for that invocation only. |
| Public certificate hostname | `door --hostname`; required lowercase `.ts.net` DNS name. |
| Bound IPv4/IPv6 addresses | Repeated `door --listen`; explicit local literals and ports, no wildcard, multicast, mapped IPv4 or zone. |
| Certificate directory / published discovery file | `door --cert-dir` / `door --wellknown-file`, both required. |
| Tailscale command | `door --tailscale-bin`, default PATH lookup; deployment scripts require an explicit absolute path. |
| Optional local discovery/proxy | Repeated `door --http-listen`, only literal loopback addresses and ports. |
| Installation health configuration | `health --backend-config` / `door --backend-config`, explicit local JSON file. |
| Account, room and device identities | Native registration/config files, `secrets/first-user.txt`, existing `secrets/matrix-admin.json`, and backend JSON `readers`; never compiled personal identities. |

The Chatty installer must supply the actual server name, public origin, runtime
layout, IPs, paths, upstream and optional health settings, and provision native
configs, registrations, credentials and LaunchAgents. This fork does not ship
that entire installer. The external matrix-server checkout must provide
`scripts/build-mautrix-native.sh`; the configured queue must implement the
existing submit/result protocol. Labels are formed from the single configured
prefix for health, service control, plist lookup and deployment scripts.

The optional `$HOME/Library/Application Support/Chatty/machine.json` is read at
startup by every command and script. It uses the flat `CHATTY_*` string keys
listed in README, with precedence flags > environment > file > defaults.
`CHATTY_MACHINE_CONFIG` selects another absolute file; invalid or explicitly
missing configuration is an error. Paths in JSON are literal absolute paths.
There is no shell evaluation, home expansion or automatic migration. Set an
existing installation's actual runtime/staging/checkout/queue/prefix once in
this file before upgrading; its existing labels and data then remain selected.
Do not copy another user's identity, addresses or private configuration.

Go and shell reject unknown keys and non-string values, including entries
overridden by the environment. Only JSON objects are accepted, not plist files.
The shell reader fails explicitly if native `plutil -extract raw -expect` is
unsupported; it never interprets a tool error as a missing setting. Privileged
redirect install/removal requires an explicit machine file or prefix through
`sudo env`, independent of root's `HOME`. Its existing fixed PF anchor remains
a singleton for compatibility: competing daemon prefixes are refused, and
removal/inspection requires the selected plist before touching/crediting rules.
Changing that prefix requires removing the old redirect first with its original
configuration. No installation or migration happens when reading the file.

Existing `door` invocations with explicit `--hostname`, `--listen`, `--cert-dir`,
`--matrix-upstream`, `--wellknown-file`, `--tailscale-bin`, `--http-listen` and
`--backend-config` retain their routing behavior. Before upgrading an existing
door with health enabled, add global `--server-name <actual-Matrix-server_name>`
**before `door`** in its argument vector. Health reports that configured identity;
startup now refuses an omitted value rather than silently reporting an example
domain. The deployment script requires `CHATTY_SERVER_NAME` and emits this flag.
If the runtime is outside the default layout, pass global `--runtime-root` too.
The actual Matrix `server_name` is also required for administrative
register/config/wipe commands. Discovery and TLS routing without health do not
require a Matrix server name.
Changing a CLI default does not migrate accounts, databases or registrations.

## Build, Config And Service Boundaries

`build.sh` is the upstream bbctl producer with its Go flags/version metadata.
`sh cmd/chatty-bridges/build.sh` builds chatty-bridges in unique staging, accepts
no arguments and prints a path only after success. `CHATTY_STAGING_ROOT` overrides
its home-relative output root. Builds bound Go compilation to `-p 2`.

The native `build` command submits a bridge build to the configured queue, never
installs it. Only `v0.2610.0` is accepted by the external builder contract. Other
tags fail before submission. The queue returns a validated job ID; inspect its
result before installing. Staging is exclusive. `install` verifies receipt tag
and SHA-256, rejects symlinks/escapes, preserves an exclusive dated backup and
atomically replaces the binary. It does not start or enable services.

`config` generates upstream and Chatty overlay templates using the selected
homeserver/server name. It copies the existing bot identity (not the appservice
sender), username template, database, permissions and secrets from native config.
It requires PostgreSQL and a double-puppet secret for the selected domain.
Output is redacted. `--diff` compares in memory and prints paths/classes only,
not values or hashes. It never writes a live config. Defaults may differ from
an existing installation and are not an authorized migration.

Launchctl exit 113 means unloaded; unknown state/error fails closed. Start uses
only regular existing plists, bootstrap/kickstart as needed, PostgreSQL first,
then the bridge. Stop unloads the bridge first. Both require readback, observing
up to 30 one-second intervals. Neither installs/enables a plist. WhatsApp
start/stop remains blocked before subprocess execution. `status` performs
read-only probes and reports installed binary versions separately when the
federation endpoint is unavailable; it does not infer the running version.

## Registration And Discovery

Registration validates the loopback homeserver and existing registration ID/URL.
A real registration also validates the configured public origin **before login
or any remote mutation**. Dry-run needs no public origin and does not read login
credentials, access the network, run processes or write markers.

The real command reads `secrets/first-user.txt`, logs in only for the operation,
finds `#admins:<server-name>` by canonical alias and sends the existing YAML. It
requires the exact response from `@conduit:<server-name>`, with a new timestamp
and placement after the sent event in the reverse chunk. It always attempts
logout. Confirmed registration atomically persists `.registered` (0600 when new)
before generating discovery; existing regular markers preserve bytes/mode.
Symlinks/special files fail. Config/registration files are not rewritten.
Partial-success errors explicitly forbid repeating registration: repair the
marker/discovery issue instead. There is no unregister command.

`wellknown` and confirmed registration share one inventory and generator:

- Output is `{"fi.mau.bridges":[...]}`, never a null array or external-server list.
- Advertise only Instagram/WhatsApp, in alphabetical order, with regular executable binaries, regular native config/registration and a regular `.registered` marker.
- Validate registration ID and fixed loopback URL, not remote state or liveness.
- Missing prerequisites omit the bridge; invalid/unreadable inventory aborts publication.
- The operator must explicitly select an HTTPS DNS origin, without credentials, explicit port, path, query or fragment. A trailing slash is normalized.
- There is no compiled public-host allowlist. The chosen origin is a trust decision: never point client provisioning at an untrusted server. HTTP inputs/request headers cannot alter it.
- Entries are `<origin>/_bridges/<bridge>`; clients append `/_matrix/provision/v3/...`.
- Without `--write`, no writes, subprocesses or network. With `--write`, use a dedicated file whose parent already exists: temporary sibling, write/chmod/sync/close, atomic rename and cleanup on failure. Preserve existing mode; new files are 0644. Symlink/special destinations fail.
- Publication is a snapshot, not a watcher. Republish explicitly after inventory changes. Wipe preserves installation/registration, so it does not remove the bridge from discovery.

Use one writer per publication path and trusted parent directories. There is no
transaction with remote registration or protection against another operator
concurrently replacing paths. An advertised stopped bridge can return 502.

## Door, TLS And Loopback HTTP

The door serves public discovery from `--wellknown-file`, rereading on each
request without cache. Missing file returns 404; unreadable file returns 503.
Known registered bridge routes proxy to fixed loopback ports 29320 (Instagram)
and 29318 (WhatsApp), removing exactly the bridge prefix. Unknown/unregistered
routes return 404, invalid inventory 503, unreachable upstream 502. Reserved
`/_chatty/backend` routes are handled by the owner, never fallback-proxied.
Other paths go to `--matrix-upstream`, including Matrix client/federation/media
and well-known client/server.

The proxy preserves Host, escaped suffix, query, Authorization, cookies and body
for the selected upstream. It recalculates forwarding headers, flushes immediately
and deliberately has no body-size cap or read-body/write timeout, preserving
uploads and long `/sync`. Header timeout is 15 seconds. There is no buffering
or deadline toggle. Logs contain only escaped method, a fixed route prefix and
status, never message bodies, request identifiers, headers or query strings.

The process runs as a regular user; redirecting public 443 to 8448 is a separate
privileged operation. It waits for all explicit local IPs before certificates or
bind, up to `--wait-timeout` (default `2m`). Failure closes previous listeners.
SIGINT/SIGTERM closes listeners and permits 10 seconds for graceful shutdown.

TLS minimum is 1.2, supporting HTTP/1.1 and HTTP/2. At startup and every 12 hours,
verify certificate hostname/pair/dates; below 30 days, call `tailscale cert` with
`--min-validity 720h0m0s` and a one-minute deadline. Tailscale must support this
flag and authorize issuance. Persist cert+private key together as
`<cert-dir>/certificate.pem` (0600, directory 0700), using private temporary
files, sync/rename then an atomic in-memory swap. Initial failure aborts;
renewal failure preserves the old certificate and emits only a generic warning.
The old certificate may expire; clients still enforce validity. One writer per
cert directory and trusted parents are required. The bundle is never public.

Optional `--http-listen` adds listeners to the same process, not another service.
Only loopback literals are allowed, with no duplicates or collisions with TLS
listeners or Matrix upstream. HTTP and TLS deliberately use separate Go Server
instances so HTTP startup cannot suppress HTTP/2 setup. All binds must succeed
before serving. Removing the flag disables this surface.

HTTP Host must be localhost, 127.0.0.1 or [::1], matching the actual listener
port (port 80 also accepts no port). Foreign authorities return 403 before
inventory/proxy. Forwarded headers cannot choose the origin. Local discovery is
generated per request from the current inventory with URLs on the validated
HTTP origin, independently of the public HTTPS file; HEAD returns no body.
A physical device's localhost is not the server Mac. HTTP availability still
depends on HTTPS startup finding its IPs and certificate; it is not an independent
offline fallback. Changing listener ownership/upstream requires a supervised
rollout, backup and rollback, never a new login to mask a routing failure.

## Backend Health Configuration

`health --backend-config <private.json> [--json]` reads local configuration each
invocation. `door --backend-config` loads it at process startup, never from HTTP
input. No configuration is discovered from personal paths or a first GET. The
local envelope uses `transport: localXPC`, `sourceOrigin: null`; this does not
implement XPC. Health is query-only: no install, adoption, pairing, service
mutation, model loading or inference. The existing `status` output is separate.

| JSON field | Contract |
| --- | --- |
| `installationID` | Explicit persistent UUID provisioned by the installation owner. |
| `validitySeconds` | Positive integer duration fitting `time.Duration`; expired collection/backward clock yields stale evidence. |
| `readers` | Explicit authorized Matrix IDs; empty denies everyone. |
| `components` | Up to 64 unique contract IDs with required dependencies present; include all ten IDs for a full installation. |

Each component probe has `id` and optional absolute normalized `binary`/`cluster`,
HTTP literal loopback `origin`, private service `token`, and PostgreSQL `host`/
`port`. PostgreSQL's binary is `pg_isready`, with a separate dedicated cluster
and endpoint per bridge; probes do not inspect schemas or database contents.
The owner must approve executables/resources, not accept arbitrary remote input.

For `tailscale`, configure a binary that actually behaves as a CLI under launchd,
not just inside an interactive terminal. The Mac App Store application's
`/Applications/Tailscale.app/Contents/MacOS/tailscale` may try to start the GUI
when launchd runs it (`Tailscale.CLIError error 3`); the probe then reports
`invalidResponse`. Where installed, the regular CLI wrapper
`/usr/local/bin/tailscale` works in that context. Verify the selected wrapper as
a launchd command before putting its absolute path in `components[].binary`.
This is configuration guidance, not permission to install a wrapper or change
the health implementation. For the door, configure its CLI path separately via
`--tailscale-bin` (or `CHATTY_TAILSCALE_BIN` in deployment scripts).

The JSON can hold a dedicated LocalAI key: keep it private and out of reports.
The synthetic fixture `internal/chatty/testdata/backend-v1/backend-config.json`
is not installable configuration. Fields other than opaque identity, component
IDs and typed evidence are not copied into responses. Inventory does not follow
symlinks, including ancestors; specify an approved regular binary/wrapper.
A Homebrew symlink alone does not prove installed-package identity. Missing
origins mean not checked, not absent. Permission errors and inaccessible sockets
do not prove a stopped process.

Tuwunel probes client versions; federation version refusal does not lower health.
Bridges separate live/ready/binary version. Dedicated LaunchAgents are observed
with print only; the running door observes itself without recursion. Tailscale
uses `status --json`; SurrealDB `/health`; LocalAI `/readyz`. The model probe
queries only `/v1/models` on the same LocalAI origin with the hub's own key.
A valid catalog lacking the contract's `embeddinggemma-2` yields modelUnavailable;
401/403/404, ambiguity/network failures do not prove absence. A catalog does not
prove load/inference. Versions null/different do not imply incompatibility.
Readiness never renews an old functional test's validity.

GET/HEAD `/_chatty/backend/v1/health` validate loopback Host/port for HTTP or the
canonical `https://<hostname>` authority for HTTPS (public port 443). Forwarded
headers are not authority. Bearer goes only to Matrix upstream account/whoami;
redirects are disabled. The returned ID must be in readers. Cookies and tailnet
membership are insufficient; session Bearer is never forwarded to service probes.
Missing/invalid credentials give 401, refused reader/Host 403, unavailable
validation/config 503, unexpected query 400. Mutating methods return 405 with
Allow before adapters, unknown reserved routes 404. HEAD performs the same
checks/status without body. JSON is no-store, with no open CORS. Errors expose
only typed code/retryability, not upstream bodies. Capabilities advertise
readHealth true and mutations false, never paths, users, keys, device IDs or logs.

## Wipe Safety Contract

Wipe is destructive and requires a separately supervised operational decision.
Its dry-run uses the existing `secrets/matrix-admin.json` administrative session,
validated by whoami, never password login. It inventories all rooms/users,
checking totals, offsets, continuity and duplicates. Matrix v12 serverless room
IDs are accepted; user IDs still require a domain. It fetches only room-member
and bridge-marker state, not timelines, discarding room/contact names and
irrelevant metadata. Reports include candidate IDs/counts and local paths/sizes,
not message contents or tokens.

Classification considers joined/invited members. A portal has the bridge's
protocol marker or local ghosts, without outsiders. A bot room has owner and bot,
without ghosts/outsiders. Mixed rooms (outsider, protected bridge identity,
conflicting protocol, or bot without owner) require manual review and are not
removed automatically. Other rooms are kept. Target and protected namespaces
must exist, use full-match RE2 semantics `\A(?:expression)\z`, and never overlap
the owner/system/other bridge. Protection is checked even for members missing
from the global user inventory, and again immediately before each purge.

The operator must approve the exact fresh list; `--confirm` is a canonical
nonnegative integer equal to the current candidate count, not an identity seal.
Any changed list needs new approval, even at equal count. Both dedicated
LaunchAgents must be unloaded, not just without a PID. Unknown state refuses.
The command does not load/stop services. It revalidates each room before DELETE
and requires independent absence afterward. Failure stops the remaining work;
there is no distributed transaction or rollback of already purged rooms. Local
state is removed only after all purges are confirmed. Use an exclusive window.

Bot and ghost users are preserved by default for reimport with the same MXIDs.
`--remove-bridge-users` instead deactivates with erase, reserving those IDs; do
not use it for reimport. Mixed rooms block this option before deletion. It never
includes owner/system/admin/protected identities. Only explicit deactivated AND
erased skips POST; readback requires both, not absent/null/false erased state.

The only removable local paths, relative to the selected runtime/bridge, are:

- `native/postgres/<bridge>`: exclusive native PostgreSQL cluster, no recovery copy; non-loopback/shared/other database layouts are refused.
- `bridges/<bridge>/logs`: old logs, without reading contents.
- `secrets/<bridge>-login-events.json`, `secrets/<bridge>-login-qr-current.png`, `secrets/<bridge>-login-qr.png`: stale login/QR state, without reading contents.

Preserve configs, registrations, database/double-puppet secrets, administrative
session, binaries, plists and every other bridge. Inspect without following
symlinks; reject special files and remove relative to `os.Root`, with readback.
Sizes sum regular-file bytes, not allocated space. WhatsApp dry-run deliberately
does not even stat its cluster; it reports size as not measured, never zero.
No production wipe, database inspection or rollout is authorized by tests.

## Sources And Verification

Configuration is composed using the existing CLI, Go filesystem/url APIs and
upstream template parameters, not a new configuration store or dependency.
The publication change only removes a machine-specific host allowlist in favor
of the operator's explicit choice; client same-origin protections, backend
reader authorization and HTTP loopback restrictions remain in force.

Primary contracts: [Go ReverseProxy](https://pkg.go.dev/net/http/httputil#ReverseProxy),
[Go net/url](https://pkg.go.dev/net/url), [urfave/cli v2 flags](https://cli.urfave.org/v2/examples/flags/),
[Matrix discovery](https://spec.matrix.org/v1.16/client-server-api/#server-discovery),
[Matrix room IDs](https://spec.matrix.org/v1.16/appendices/#room-ids),
[RE2 syntax](https://pkg.go.dev/regexp/syntax), and
[Tailscale cert](https://github.com/tailscale/tailscale/blob/main/cmd/tailscale/cli/cert.go).
Tuwunel administrative API contracts were checked against v1.9.2/v1.9.4:
[users](https://github.com/matrix-construct/tuwunel/blob/v1.9.4/src/api/client/admin/users/list_users.rs),
[room deletion](https://github.com/matrix-construct/tuwunel/blob/v1.9.4/src/api/client/admin/rooms/delete_room.rs),
[filtered state](https://github.com/matrix-construct/tuwunel/blob/v1.9.4/src/api/client/admin/rooms/state.rs),
[deactivation](https://github.com/matrix-construct/tuwunel/blob/v1.9.4/src/api/client/admin/users/deactivate_account.rs).
Do not infer a running server version from source tags alone.

Run `go test -p 2 ./...`, `go build -p 2 ./...`, focused `go vet`, gofmt and
shell syntax/ShellCheck with bounded parallelism. Tests canonicalize only their
temporary fixture roots, accepting the default macOS TMPDIR alias while retaining
deliberately created symlinks and production path-safety rejection. Run the suite
with both unresolved and resolved TMPDIR. Offline tests cover real loopback HTTP IPv4/IPv6, synthetic TLS/HTTP2,
streaming, uploads, certificate renewal, authorization, publication atomicity,
registration continuity, wipe boundaries and fake launchctl. Backend shared
vectors live in `internal/chatty/testdata/backend-v1/`. No test uses a real
account/device, production credential, certificate issuance or service mutation.
Successful tests are not deployment, GUI acceptance or permission for rollout.
