#!/bin/sh
# Shared data-only configuration; never source or evaluate the user's JSON.
if [ "${CHATTY_MACHINE_CONFIG+x}" != x ]; then
    case ${HOME:-} in
        /*) ;;
        *) printf '%s\n' 'HOME must be a nonempty absolute path, or set CHATTY_MACHINE_CONFIG explicitly.' >&2; exit 1 ;;
    esac
fi
chatty_machine_config=${CHATTY_MACHINE_CONFIG-${HOME:-}/Library/Application Support/Chatty/machine.json}
case $chatty_machine_config in
    /*) ;;
    *) printf '%s\n' 'CHATTY_MACHINE_CONFIG must be an absolute path.' >&2; exit 1 ;;
esac
if [ -e "$chatty_machine_config" ] || [ -L "$chatty_machine_config" ] || [ "${CHATTY_MACHINE_CONFIG+x}" = x ]; then
    chatty_json=$(/bin/cat "$chatty_machine_config") || {
        printf 'Cannot read CHATTY_MACHINE_CONFIG: %s\n' "$chatty_machine_config" >&2
        exit 1
    }
    # Require JSON rather than plutil's other formats. The wrapper lets plutil
    # enumerate the root dictionary without parsing its human-readable output.
    # JSON whitespace includes literal tab, carriage return and newline.
    chatty_space=$(printf ' \t\r\n.')
    chatty_space=${chatty_space%.}
    while [ -n "$chatty_json" ]; do
        case $chatty_json in
            ["$chatty_space"]*) chatty_json=${chatty_json#?} ;;
            *) break ;;
        esac
    done
    case $chatty_json in
        \{*) ;;
        *) printf '%s\n' 'CHATTY_MACHINE_CONFIG must be a JSON object of string values.' >&2; exit 1 ;;
    esac
    chatty_json=$(printf '%s' "$chatty_json" | /usr/bin/plutil -convert json -o - - 2>/dev/null) || {
        printf 'Cannot parse CHATTY_MACHINE_CONFIG as JSON: %s\n' "$chatty_machine_config" >&2
        exit 1
    }
    chatty_json='{"settings":'"$chatty_json"'}'
    chatty_keys=$(printf '%s' "$chatty_json" | /usr/bin/plutil -extract settings raw -expect dictionary -o - - 2>/dev/null) || {
        printf '%s\n' 'Cannot enumerate CHATTY_MACHINE_CONFIG; plutil must support -extract raw -expect dictionary.' >&2
        exit 1
    }
    chatty_remaining=$chatty_json
    while IFS= read -r chatty_key; do
        if [ -z "$chatty_key" ] && [ "$chatty_json" = '{"settings":{}}' ]; then continue; fi
        # Keep in sync with machineSettingKeys; TestMachineReaderKnownKeys checks both readers.
        case $chatty_key in
            CHATTY_RUNTIME_ROOT|CHATTY_SERVER_REPO|CHATTY_STAGING_ROOT|CHATTY_BUILD_QUEUE|CHATTY_HOMESERVER|CHATTY_SERVER_NAME|CHATTY_PUBLIC_BASE_URL|CHATTY_LAUNCHD_PREFIX|CHATTY_HOSTNAME|CHATTY_MATRIX_UPSTREAM|CHATTY_TAILNET_IPV6_PREFIX|CHATTY_TAILSCALE_BIN|CHATTY_BINARY|CHATTY_CERT_DIR|CHATTY_WELLKNOWN_FILE|CHATTY_HTTP_LISTEN_IPV4|CHATTY_HTTP_LISTEN_IPV6|CHATTY_BACKEND_CONFIG|CHATTY_CERTIFICATE) ;;
            *) printf '%s\n' 'CHATTY_MACHINE_CONFIG contains an unknown setting.' >&2; exit 1 ;;
        esac
        # A sentinel preserves trailing newlines in a string. Extraction failures
        # are fatal even under plain sh and even when the environment overrides it.
        chatty_value=$(printf '%s' "$chatty_json" | /usr/bin/plutil -extract "settings.$chatty_key" raw -expect string -o - - 2>/dev/null && printf '.') || {
            printf 'CHATTY_MACHINE_CONFIG: cannot extract %s as a string.\n' "$chatty_key" >&2
            exit 1
        }
        chatty_value=${chatty_value%.}
        chatty_newline='
'
        chatty_value=${chatty_value%"$chatty_newline"}
        chatty_remaining=$(printf '%s' "$chatty_remaining" | /usr/bin/plutil -remove "settings.$chatty_key" -o - - 2>/dev/null) || {
            printf '%s\n' 'Cannot validate all CHATTY_MACHINE_CONFIG keys.' >&2
            exit 1
        }
        if /usr/bin/printenv "$chatty_key" >/dev/null; then continue; fi
        export "$chatty_key=$chatty_value"
    done <<EOF
$chatty_keys
EOF
    # Raw key enumeration cannot represent an empty key. Nothing unvalidated may remain.
    [ "$chatty_remaining" = '{"settings":{}}' ] || {
        printf '%s\n' 'CHATTY_MACHINE_CONFIG contains an unknown setting.' >&2
        exit 1
    }
fi
: "${CHATTY_LAUNCHD_PREFIX:=local.chatty}"
case $CHATTY_LAUNCHD_PREFIX in
    *[!a-zA-Z0-9.-]*|[!a-zA-Z0-9]*|'')
        printf '%s\n' 'Invalid CHATTY_LAUNCHD_PREFIX: use ASCII letters, digits, dots and hyphens; start with a letter or digit.' >&2
        exit 1 ;;
esac
export CHATTY_LAUNCHD_PREFIX
unset chatty_machine_config chatty_json chatty_remaining chatty_keys chatty_key chatty_value chatty_space chatty_newline

# The fixed PF anchor is intentionally a singleton to preserve existing installs.
# See tailnet-door.md: remove the old install before changing its label prefix.
chatty_redirect_owner() {
    [ -d "$1" ] && [ -r "$1" ] && [ -x "$1" ] || {
        printf '%s\n' 'Cannot inspect redirect LaunchDaemon ownership.' >&2
        return 1
    }
    for chatty_plist in "$1/"*-tailnet-redirect.plist; do
        [ -e "$chatty_plist" ] || [ -L "$chatty_plist" ] || continue
        if [ "$chatty_plist" != "$1/$2.plist" ]; then
            printf 'Conflicting redirect installation: %s; remove it with its original machine configuration first.\n' "$chatty_plist" >&2
            return 1
        fi
    done
    if [ "$3" = required ] && { [ ! -f "$1/$2.plist" ] || [ -L "$1/$2.plist" ]; }; then
        printf 'No redirect plist for %s; refusing to touch the shared PF anchor.\n' "$2" >&2
        return 1
    fi
    unset chatty_plist
}
