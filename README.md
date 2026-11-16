# Chatty Bridges

An Apache-2.0 adaptation of [Beeper Bridge Manager](https://github.com/beeper/bridge-manager)
for native Tuwunel installations. `chatty-bridges` manages the Instagram and
WhatsApp bridge inventory without Beeper authentication, hungryserv or the
appservice websocket proxy. The upstream `bbctl` entrypoint remains separate.

## Build And Configure

Requires macOS and Go 1.25 or newer. This CLI does not install a homeserver,
PostgreSQL, bridge configuration, credentials or LaunchAgents for you.

```sh
GOMAXPROCS=2 nice -n 10 go build -p 2 ./...
GOMAXPROCS=2 nice -n 10 sh cmd/chatty-bridges/build.sh
```

The second command prints a unique staging binary path only after a successful
build. Set `CHATTY_STAGING_ROOT` to override its output root; otherwise it uses
`$HOME/Library/Caches/Chatty/bridge-manager`. It accepts no arguments.
`build.sh` remains the upstream `bbctl` builder, including its Go arguments and
version metadata.

Global flags go **before** the command:

| Flag | Default / meaning |
| --- | --- |
| `--homeserver` | `http://127.0.0.1:8008`, the administrative HTTP loopback origin. |
| `--server-name` | No default. Actual Matrix `server_name`, not its public DNS hostname; required for config/register/wipe/health and `door --backend-config`. |
| `--runtime-root` | `$HOME/Library/Application Support/Chatty/runtime`; owns `bridges/`, `secrets/` and `native/`. |
| `--server-repo` | `$HOME/Library/Application Support/Chatty/matrix-server`; checkout providing `scripts/build-mautrix-native.sh`. |
| `--staging-root` | `$HOME/Library/Caches/Chatty/bridge-manager`; exclusive bridge build staging. |
| `--build-queue` | No default. Executable implementing the existing queue submit/result protocol; `build` refuses an unset value before creating staging or running a process. |
| `--launchd-prefix` | `local.chatty`; all managed labels are `<prefix>-<component>`. |
| `--public-base-url` | No default. Explicit public HTTPS origin for registration output and discovery. |

Home-relative defaults are resolved using the current user's home at startup;
`~` inside a quoted flag value is not expanded by the CLI. Use `"$HOME/..."`.
Labels include `<prefix>-tuwunel`, `<prefix>-door`, `<prefix>-whatsapp-bridge`,
`<prefix>-instagram-bridge`, `<prefix>-whatsapp-postgres`,
`<prefix>-instagram-postgres` and `<prefix>-tailnet-redirect`.
The server checkout and queue are external prerequisites for `build`, not bundled
with this fork. Use explicit paths when installing elsewhere.

### One Configuration Per Machine

Every CLI invocation and deployment/build script reads the optional per-user
`$HOME/Library/Application Support/Chatty/machine.json`. No shell startup file
or interactive terminal is needed, including when launchd starts the CLI.
`CHATTY_MACHINE_CONFIG=/absolute/path/machine.json` selects a different file;
an explicitly selected missing file or invalid configuration fails closed.
Precedence is **CLI flags > environment > machine file > neutral defaults**.
The JSON is a flat object of string values, not executable shell code. Use full
absolute paths; neither `$HOME` nor `~` is expanded inside JSON. For example:

```json
{
  "CHATTY_RUNTIME_ROOT": "/Users/example/Library/Application Support/Chatty/runtime",
  "CHATTY_SERVER_REPO": "/Users/example/Projects/matrix-server",
  "CHATTY_STAGING_ROOT": "/Users/example/Library/Caches/Chatty/bridge-manager",
  "CHATTY_BUILD_QUEUE": "/absolute/path/to/queue-submit",
  "CHATTY_LAUNCHD_PREFIX": "org.example.chatty",
  "CHATTY_SERVER_NAME": "matrix.example.org",
  "CHATTY_PUBLIC_BASE_URL": "https://node.example.ts.net"
}
```

The global settings map to `CHATTY_HOMESERVER`, `CHATTY_SERVER_NAME`,
`CHATTY_RUNTIME_ROOT`, `CHATTY_SERVER_REPO`, `CHATTY_STAGING_ROOT`,
`CHATTY_BUILD_QUEUE`, `CHATTY_PUBLIC_BASE_URL` and `CHATTY_LAUNCHD_PREFIX`.
Deployment scripts also read their documented `CHATTY_*` settings from this
same file, using macOS `plutil`, without sourcing/evaluating it. Protect the file
and parent directory as user-owned configuration (recommended 0600/0700); do not
store credentials there. CLI-specific door flags remain explicit in installed
plists. For privileged install/removal, select the owning user's file explicitly:
`sudo env CHATTY_MACHINE_CONFIG=/absolute/path/machine.json /bin/bash deploy/macos/<script>.sh ...`.
The installed root helper instead receives a frozen prefix from its root-owned
plist and never reads the user's file on boot.

Both readers reject unknown keys, non-string values and non-JSON formats, even
when an environment value overrides the entry. A misspelled prefix never silently
selects the neutral namespace. The shell reader requires macOS `plutil` support
for `-extract raw -expect`; unsupported operations fail visibly, not as absent
keys. Without an explicit file, `HOME` must be a nonempty absolute path.
An explicitly empty `CHATTY_STAGING_ROOT` makes the build script fail before
creating output, with a diagnostic naming the setting.

Privileged redirect installation/removal refuses to infer a machine from root's
home: pass `CHATTY_MACHINE_CONFIG` or `CHATTY_LAUNCHD_PREFIX` explicitly through
`sudo env`. The existing PF anchor is a singleton. A second redirect prefix is
refused; removal and inspection require the selected daemon plist and reject a
competing installation. Remove the old redirect using its original configuration
before changing prefixes. See [the macOS guide](deploy/macos/tailnet-door.md).

Before upgrading an existing installation, record its actual runtime, staging,
server checkout, queue and existing launchd prefix in this file. Otherwise the
new defaults intentionally select a different installation. Nothing migrates,
renames or restarts existing services automatically. Keep the existing prefix
to manage the same labels; change it only with an approved migration. Prefixes
use ASCII letters, digits, dots and hyphens, starting with a letter or digit.
Build automation supplies `--card <id>` or `CHATTY_BUILD_CARD`; there is no
dependency on any particular task-management product.

The Chatty installer is responsible for filling in the chosen runtime and binary
paths, Matrix `server_name`, public hostname/origin, local IPv4/IPv6 listeners,
upstream, certificate and discovery paths, Tailscale executable and optional
backend-config. It must also provision native bridge configs/registrations,
credentials, PostgreSQL and the corresponding LaunchAgents. The CLI does not
infer these values from a developer's machine. The scripts in `deploy/macos/`
configure only the door and its optional privileged redirect.

```sh
chatty-bridges --help
chatty-bridges --server-name matrix.example.org --runtime-root "$HOME/.local/share/chatty/runtime" status instagram
chatty-bridges --server-name matrix.example.org register instagram --dry-run
chatty-bridges --server-name matrix.example.org config instagram --diff
chatty-bridges --public-base-url https://node.example.ts.net wellknown
chatty-bridges wellknown --base-url https://node.example.ts.net --write /absolute/path/mautrix.json
```

## Native Operations

`config <bridge>` composes upstream templates with `bridgeconfig/chatty.tpl.yaml`,
using the configured homeserver and server name. It preserves the native bot
identity, database, secrets, permissions and username template. Output is always
redacted, not installable YAML. `--diff` prints only structural paths/classes,
never values or secret hashes. Neither form overwrites live configuration.

`register <bridge> --dry-run` reads only the local registration and prints a
redacted plan, without login, network, marker writes or subprocesses. A real
registration requires `--public-base-url` and the correct `--server-name`:

```sh
chatty-bridges --server-name matrix.example.org --public-base-url https://node.example.ts.net register instagram
```

It reads `secrets/first-user.txt` under the runtime, logs into the loopback
homeserver, finds `#admins:<server-name>`, sends the existing appservice
registration and checks the exact ID, administrative sender and event ordering.
It then atomically persists `.registered`, prints discovery and logs out.
Do not repeat registration when the error says it was confirmed but local marker
persistence or discovery failed; repair only the reported local failure.

`build <bridge> --tag v0.2610.0 --card <id>` submits to the configured queue and
reports a job and source directory. Only this tag is supported by the external
native builder. Submission is not build completion. Inspect the queue result
before `install <bridge> --source <staging/source>`. Installation verifies the
receipt's tag/SHA-256, rejects staging symlinks, preserves an exclusive backup
and atomically replaces the binary. It does not start services.

`service status|start|stop <bridge>` uses existing user LaunchAgents. Start loads
PostgreSQL before the bridge; stop unloads the bridge before PostgreSQL. Unknown
state fails closed, and effects require readback. WhatsApp start/stop remain
blocked by the existing safety policy. `status` performs read-only health GETs
and process/version queries. An installed binary version is not proof of the
running version. None of these commands authorizes a production rollout.

## Discovery And The Private Door

`wellknown` emits only `{"fi.mau.bridges":[...]}`. Choose the public origin via
`--base-url` on this command, or global `--public-base-url`; a local `--base-url`
overrides the global value. Missing or malformed origins fail before publication.
The operator selects the trusted DNS hostname; there is no compiled host allowlist.
HTTPS is required, without an explicit port, credentials, path, query or fragment;
a final slash is accepted. Do not select an untrusted origin: clients may send
Matrix credentials to provisioning URLs there. Request headers never select it.

Only regular executable bridge binaries with regular config/registration files
and a regular `.registered` marker are advertised, alphabetically. Registration
ID and loopback URL must match the bridge. Inventory errors abort publication;
missing prerequisites omit that bridge. No remote registration/health claim is
made. `--write` uses a temporary file, sync and atomic rename beside a dedicated
existing-parent destination; it refuses symlinks and preserves the previous file
on failure. Republish after inventory changes; there is no watcher.

Example configuration, not a command to run against an existing installation:

```sh
chatty-bridges --server-name matrix.example.org --runtime-root "$HOME/.local/share/chatty/runtime" door \
  --listen 100.64.0.10:8448 \
  --listen '[fd00:1234:5678::10]:8448' \
  --hostname node.example.ts.net \
  --cert-dir "$HOME/.local/share/chatty/runtime/secrets/chatty-door" \
  --matrix-upstream http://127.0.0.1:8009 \
  --wellknown-file "$HOME/.local/share/chatty/runtime/config/mautrix.json" \
  --tailscale-bin /absolute/path/to/tailscale \
  --http-listen 127.0.0.1:8008 --http-listen '[::1]:8008' \
  --backend-config /absolute/path/to/backend-config.json
```

Replace all synthetic addresses with the node's actual authorized values. TLS
listeners require explicit local IPs, never wildcards or DNS. `door` waits up to
`--wait-timeout` (default `2m`) for every IP. It runs without root. Public TCP 443
requires a separately supervised redirect to 8448, not installed by this CLI.
`--matrix-upstream` defaults to `http://127.0.0.1:8008`, and `--tailscale-bin` to
`tailscale` through PATH. All hostname, certificate and discovery paths are
explicit. `--http-listen` and `--backend-config` are optional; retain both when
re-rendering an installation that already uses them. See the
[macOS deployment guide](deploy/macos/tailnet-door.md).

Before upgrading a door with health enabled, add global `--server-name <actual-Matrix-server_name>`
before `door` in the existing argument vector. Omission now fails at startup;
there is no example-domain fallback. The deployment script takes this value from
required `CHATTY_SERVER_NAME`. Without health, TLS routing does not need it.

| Route | Destination |
| --- | --- |
| `/.well-known/matrix/mautrix` over HTTPS | Published file, reread without cache; 404 if absent, 503 if unreadable. |
| Same route over optional loopback HTTP | Current inventory, URLs on the validated request origin. |
| `/_bridges/instagram[/...]` | `http://127.0.0.1:29320`, removing the bridge prefix. |
| `/_bridges/whatsapp[/...]` | `http://127.0.0.1:29318`, removing the bridge prefix. |
| Unknown/unregistered bridge | 404; invalid inventory gives 503. |
| `/_chatty/backend/v1/health` | Authenticated owner health route, never Matrix proxy fallback. |
| Everything else | Matrix upstream. |

The proxy preserves Host, query, credentials, escaped paths and bodies, while
recomputing forwarding headers. Immediate flushing and no body/write timeout
preserve streaming, uploads and long `/sync` requests. Request logs include only
method, fixed route prefix and status, not tokens, queries or message content.
TLS is at least 1.2. Certificates are obtained with `tailscale cert`, verified
and atomically stored with the private key in `<cert-dir>/certificate.pem` (0600,
directory 0700). Initial failure aborts. Renewal checks every 12 hours and asks
for at least 30 days' validity; failure preserves the old certificate, which can
still expire. Use one writer per cert directory.

## Backend Health And Cleanup

```sh
chatty-bridges --server-name matrix.example.org health --backend-config /absolute/private/backend-config.json --json
chatty-bridges --server-name matrix.example.org wipe whatsapp --dry-run
```

Health requires explicit installation identity, authorized readers and local probe
configuration. See [CHATTY.md](CHATTY.md) for fields, authorization and CLI-wrapper
requirements for Tailscale under launchd. Readiness is not a functional test.

Wipe uses the existing administrative session in `secrets/matrix-admin.json`,
without password login or timeline reads. It inventories all rooms/users,
protects other bridges and mixed rooms, and reports a plan. The WhatsApp dry-run
does not inspect its PostgreSQL directory. Destructive execution requires human
approval of the exact current list, both LaunchAgents unloaded and
`--confirm <room-count>`. Count alone does not bind the approved identities.
Bot/ghost MXIDs are preserved by default. `--remove-bridge-users` irreversibly
deactivates/erases and reserves them; do not use it for reimport. Configs,
registrations, credentials, binaries, plists and other bridges are preserved.
See [CHATTY.md](CHATTY.md) for the removal allowlist and failure semantics.

## Verification And Upstream

```sh
GOMAXPROCS=2 nice -n 10 go test -p 2 ./...
GOMAXPROCS=2 nice -n 10 go vet -p 2 ./internal/chatty/... ./cmd/chatty-bridges/...
```

Tests resolve temporary fixture roots themselves, so both default macOS
`/var/folders/...` TMPDIR and its canonical `/private/var/...` form work without
weakening production symlink rejection. Tests use synthetic files, credentials, loopback HTTP,
TLS certificates and fake process adapters. They do not operate production
services, issue real certificates or prove a deployed installation healthy.

## Upstream Documentation (bbctl, Not Tuwunel)

# Beeper Bridge Manager
A tool for running self-hosted bridges with the Beeper Matrix server.

The primary use case is running custom/3rd-party bridges with Beeper. You can
connect any<sup>†</sup> spec-compliant Matrix application service to your Beeper
account without having to self-host a whole Matrix homeserver. Note that if you
run 3rd party bridges that don't support end-to-bridge encryption, message
contents will be visible to Beeper servers.

<sub>†caveat: hungryserv does not implement the entire Matrix client-server API, so
it's possible some bridges won't work - you can report such cases in the
self-hosting support room linked below or in GitHub issues here</sub>

You can also self-host the official bridges for maximum security using this
tool (so that message re-encryption happens on a machine you control rather
than on Beeper servers).

This tool can not be used with any other Matrix homeserver, like self-hosted
Synapse instances. It is only for connecting self-hosted bridges to the
beeper.com server. For self-hosting the entire stack, refer to the official
documentation of the various projects
([Synapse](https://element-hq.github.io/synapse/latest/),
[mautrix bridges](https://docs.mau.fi/bridges/)).

> [!NOTE]
> Self-hosted bridges are not entitled to the usual level of customer support
> on Beeper. If you need help with self-hosting bridges using this tool, please
> join [#self-hosting:beeper.com] instead of asking in your support room.

[#self-hosting:beeper.com]: https://matrix.to/#/#self-hosting:beeper.com

## Usage
1. Install bbctl with Homebrew: `brew install beeper/tap/bbctl`.
   * Alternatively, you can download the latest binary from [GitHub releases](https://github.com/beeper/bridge-manager/releases)
     or [actions](https://nightly.link/beeper/bridge-manager/workflows/go.yaml/main).
   * You can also build bbctl yourself with `go build -p 2 ./cmd/bbctl`.
     The separate Chatty builder is `sh cmd/chatty-bridges/build.sh`.
     Building requires Go 1.25 or higher.
   * bbctl supports amd64 and arm64 on Linux and macOS.
     Windows is not supported natively, please use WSL.
2. Log into your Beeper account with `bbctl login`.

Then continue with one of the sections below, depending on whether you want to
run an official Beeper bridge or a 3rd party bridge.

### Official bridges
For Python bridges, you must install Python 3 with the `venv` module with your
OS package manager. For example, `sudo apt install python3 python3-venv` on
Debian-based distros. The Python version built into macOS may be new enough, or
you can get the latest version via brew. The minimum Python version varies by
bridge, but if you use the latest Debian or Ubuntu LTS, it should be new enough.

Some bridges require ffmpeg for converting media (e.g. when sending gifs), so
you should also install that with your OS package manager (`sudo apt install ffmpeg`
on Debian or `brew install ffmpeg` on macOS).

After installing relevant dependencies:

3. Run `bbctl run <name>` to run the bridge.
   * `<name>`  should start with `sh-` and consist of a-z, 0-9 and -.
   * If `<name>` contains the bridge type, it will be automatically detected.
     Otherwise pass the type with `--type <type>`.
   * See the table below for supported official bridges.
   * The bridge will be installed to `~/.local/share/bbctl`. You can change the
     directory in the config file at `~/.config/bbctl.json`.
4. For now, you'll have to configure the bridge by sending a DM to the bridge
   bot (`@<name>bot:beeper.local`). Configuring self-hosted bridges through the
   chat networks dialog will be available in the future. Spaces and starting
   chats are also not yet available, although you can start chats using the
   `pm` command with the bridge bot.

There is currently a bug in Beeper Desktop that causes it to create encrypted
DMs even if the recipient doesn't support it. This means that for non-e2ee-
capable bridges like Heisenbridge, you'll have to create the DM with the bridge
bot in another Matrix client, or using the create group chat button in Beeper
Desktop.

Currently the bridge will run in foreground, so you'll have to keep `bbctl run`
active somewhere (tmux is a good option). In the future, a service mode will be
added where the bridge is registered as a systemd or launchd service to be
started automatically by the OS.

#### Official bridge list
When using `bbctl run` or `bbctl config` and the provided `<name>` contains one
of the identifiers (second column) listed below, bbctl will automatically guess
that type. A substring match is sufficient, e.g. `sh-mywhatsappbridge` will
match `whatsapp`. The first listed identifier is the "primary" one that can be
used with the `--type` flag.

| Bridge               | Identifier                           |
|----------------------|--------------------------------------|
| [mautrix-telegram]   | telegram                             |
| [mautrix-whatsapp]   | whatsapp                             |
| [mautrix-signal]     | signal                               |
| [mautrix-discord]    | discord                              |
| [mautrix-slack]      | slack                                |
| [mautrix-gmessages]  | gmessages,  googlemessages, rcs, sms |
| [mautrix-gvoice]     | gvoice, googlevoice                  |
| [mautrix-meta]       | meta, instagram, facebook            |
| [mautrix-googlechat] | googlechat, gchat                    |
| [mautrix-twitter]    | twitter                              |
| [mautrix-bluesky]    | bluesky, bsky                        |
| [mautrix-imessage]   | imessage                             |
| [beeper-imessage]    | imessagego                           |
| [mautrix-linkedin]   | linkedin                             |
| [heisenbridge]       | heisenbridge, irc                    |

[mautrix-telegram]: https://github.com/mautrix/telegram
[mautrix-whatsapp]: https://github.com/mautrix/whatsapp
[mautrix-signal]: https://github.com/mautrix/signal
[mautrix-discord]: https://github.com/mautrix/discord
[mautrix-slack]: https://github.com/mautrix/slack
[mautrix-gmessages]: https://github.com/mautrix/gmessages
[mautrix-gvoice]: https://github.com/mautrix/gvoice
[mautrix-meta]: https://github.com/mautrix/meta
[mautrix-googlechat]: https://github.com/mautrix/googlechat
[mautrix-twitter]: https://github.com/mautrix/twitter
[mautrix-bluesky]: https://github.com/mautrix/bluesky
[mautrix-imessage]: https://github.com/mautrix/imessage
[beeper-imessage]: https://github.com/beeper/imessage
[mautrix-linkedin]: https://github.com/mautrix/linkedin
[heisenbridge]: https://github.com/hifi/heisenbridge

### 3rd party bridgev2-based bridges
If you have a 3rd party bridge that's built on top of mautrix-go's bridgev2
framework, you can have bbctl generate a mostly-complete config file:

3. Run `bbctl config --type bridgev2 <name>` to generate a bridgev2 config with
   everything except the `network` section.
   * `<name>` is a short name for the bridge (a-z, 0-9, -). The name should
     start with `sh-`. The bridge user ID namespace will be `@<name>_.+:beeper.local`
     and the bridge bot will be `@<name>bot:beeper.local`.
4. Add the `network` section containing the bridge-specific configuration if
   necessary, then run the bridge normally.

All bridgev2 bridges support appservice websockets, so using `bbctl proxy` is
not necessary.

### 3rd party custom bridges
For any 3rd party bridges that don't use bridgev2, you'll only get a registration
file from bbctl and will have to configure the bridge yourself. Also, since such
3rd party bridges are unlikely to support Beeper's appservice websocket protocol,
you probably have to use `bbctl proxy` to connect to the websocket and turn
incoming data into HTTP requests for the bridge.

3. Run `bbctl register <name>` to generate an appservice registration file.
   * `<name>` is the same as in the above section.
4. Now you can configure and run the bridge by following the bridge's own
   documentation.
5. Modify the registration file to point at where the bridge will listen locally
   (e.g. `url: http://localhost:8080`), then run `bbctl proxy -r registration.yaml`
   to start the proxy.
   * The proxy will connect to the Beeper server using a websocket and push
     received events to the bridge via HTTP. Since the HTTP requests are all on
     localhost, you don't need port forwarding or TLS certificates.

Note that the homeserver URL is not guaranteed to be stable forever, it has
changed in the past, and it may change again in the future.

You can use `--json` with `register` to get the whole response as JSON instead
of registration YAML and pretty-printed extra details. This may be useful if
you want to automate fetching the homeserver URL.

### Deleting bridges
If you don't want a self-hosted bridge anymore, you can delete it using
`bbctl delete <name>`. Deleting a bridge will permanently erase all traces of
it from the Beeper servers (e.g. any rooms and ghost users it created).
For official bridges, it will also delete the local data directory with the
bridge config, database and python virtualenv (if applicable).

Note that deleting a bridge through the Beeper client settings will
*not* delete the bridge database that is stored locally; you must
delete that yourself, or use `bbctl delete` instead. (If you created
the bridge database with `bbctl run -l`, then run `bbctl delete -l`
from the same working directory to delete it.)

If you later re-add a self-hosted bridge after deleting it from the
Beeper servers but not deleting the local database, you should expect
errors, as the bridge will have been removed from Matrix rooms that it
thinks it is a member of.
