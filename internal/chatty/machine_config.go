package chatty

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const DefaultLaunchdPrefix = "local.chatty"

var launchdPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)

// Keep the shell reader's allowlist in sync; TestMachineReaderKnownKeys checks both readers.
var machineSettingKeys = []string{
	"CHATTY_RUNTIME_ROOT", "CHATTY_SERVER_REPO", "CHATTY_STAGING_ROOT", "CHATTY_BUILD_QUEUE",
	"CHATTY_HOMESERVER", "CHATTY_SERVER_NAME", "CHATTY_PUBLIC_BASE_URL", "CHATTY_LAUNCHD_PREFIX",
	"CHATTY_HOSTNAME", "CHATTY_MATRIX_UPSTREAM", "CHATTY_TAILNET_IPV6_PREFIX", "CHATTY_TAILSCALE_BIN",
	"CHATTY_BINARY", "CHATTY_CERT_DIR", "CHATTY_WELLKNOWN_FILE", "CHATTY_HTTP_LISTEN_IPV4",
	"CHATTY_HTTP_LISTEN_IPV6", "CHATTY_BACKEND_CONFIG", "CHATTY_CERTIFICATE",
}

func machineSettings(home string) (map[string]string, error) {
	path, explicit := os.LookupEnv("CHATTY_MACHINE_CONFIG")
	if !explicit {
		path = filepath.Join(home, "Library/Application Support/Chatty/machine.json")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("CHATTY_MACHINE_CONFIG must be an absolute path")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read CHATTY_MACHINE_CONFIG %q: %w", path, err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, errors.New("CHATTY_MACHINE_CONFIG must be a JSON object of string values")
	}
	settings := make(map[string]string, len(fields))
	for key, raw := range fields {
		known := false
		for _, allowed := range machineSettingKeys {
			if key == allowed {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("CHATTY_MACHINE_CONFIG: unknown setting %q", key)
		}
		var value string
		if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
			return nil, errors.New("CHATTY_MACHINE_CONFIG must be a JSON object of string values")
		}
		settings[key] = value
	}
	return settings, nil
}

func (config Config) launchdLabel(suffix string) string {
	prefix := config.LaunchdPrefix
	if prefix == "" {
		prefix = DefaultLaunchdPrefix
	}
	return prefix + "-" + suffix
}
