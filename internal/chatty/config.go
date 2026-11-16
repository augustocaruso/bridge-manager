package chatty

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/beeper/bridge-manager/bridgeconfig"
)

func mergeMap(target, overlay map[string]any) {
	for key, value := range overlay {
		sourceMap, isMap := value.(map[string]any)
		targetMap, targetIsMap := target[key].(map[string]any)
		if isMap && targetIsMap {
			mergeMap(targetMap, sourceMap)
		} else {
			target[key] = value
		}
	}
}
func mapAt(root map[string]any, path ...string) map[string]any {
	for _, key := range path {
		child, ok := root[key].(map[string]any)
		if !ok {
			child = map[string]any{}
			root[key] = child
		}
		root = child
	}
	return root
}
func (m *Manager) nativeConfig(b Bridge) (map[string]any, map[string]any, error) {
	if err := m.requireServerName(); err != nil {
		return nil, nil, err
	}
	if err := validateHomeserver(m.Config.Homeserver); err != nil {
		return nil, nil, err
	}
	live, err := readYAML(m.bridgePath(b, "config.native.yaml"))
	if err != nil {
		return nil, nil, err
	}
	registration, err := m.registration(b)
	if err != nil {
		return nil, nil, err
	}
	generated, err := bridgeconfig.Generate(b.Name, bridgeconfig.Params{HungryAddress: m.Config.Homeserver, BeeperDomain: m.Config.ServerName, BridgeName: b.Name, AppserviceID: b.Name, ASToken: "placeholder-as", HSToken: "placeholder-hs", ProvisioningSecret: "placeholder-provision"})
	if err != nil {
		return nil, nil, errors.New("invalid upstream template")
	}
	var config map[string]any
	if yaml.Unmarshal([]byte(generated), &config) != nil {
		return nil, nil, errors.New("invalid upstream template YAML")
	}
	overlay, err := bridgeconfig.Generate("chatty", bridgeconfig.Params{HungryAddress: m.Config.Homeserver, BeeperDomain: m.Config.ServerName})
	if err != nil {
		return nil, nil, errors.New("native overlay unavailable")
	}
	var native map[string]any
	if yaml.Unmarshal([]byte(overlay), &native) != nil {
		return nil, nil, errors.New("invalid native overlay")
	}
	mergeMap(config, native)
	mapAt(config, "homeserver")["address"] = m.Config.Homeserver
	mapAt(config, "homeserver")["domain"] = m.Config.ServerName
	appservice := mapAt(config, "appservice")
	appservice["id"] = b.Name
	appservice["address"] = fmt.Sprintf("http://127.0.0.1:%d", b.Port)
	appservice["port"] = b.Port
	appservice["as_token"] = registration["as_token"]
	appservice["hs_token"] = registration["hs_token"]
	bot, ok := mapAt(live, "appservice", "bot")["username"].(string)
	if !ok || bot == "" {
		return nil, nil, errors.New("bot identity missing in native config")
	}
	mapAt(appservice, "bot")["username"] = bot
	for _, path := range [][]string{{"database", "type"}, {"database", "uri"}, {"provisioning", "shared_secret"}, {"double_puppet", "secrets"}, {"encryption", "pickle_key"}, {"public_media", "signing_key"}, {"direct_media", "server_key"}, {"bridge", "permissions"}, {"appservice", "username_template"}} {
		parent := mapAt(live, path[0])
		value, ok := parent[path[1]]
		if !ok {
			return nil, nil, errors.New("live config missing required field: " + strings.Join(path, "."))
		}
		mapAt(config, path[0])[path[1]] = value
	}
	if mapAt(config, "database")["type"] != "postgres" {
		return nil, nil, errors.New("native config requires PostgreSQL")
	}
	secrets := mapAt(config, "double_puppet", "secrets")
	if secret, ok := secrets[m.Config.ServerName].(string); !ok || !strings.HasPrefix(secret, "as_token:") || len(secret) <= len("as_token:") {
		return nil, nil, errors.New("native double puppet not configured")
	}
	mapAt(config, "double_puppet")["servers"] = map[string]any{}
	mapAt(config, "network")["os_name"] = "Chatty"
	if b.Name == "instagram" {
		delete(mapAt(config, "network"), "os_name")
	}
	return config, live, nil
}

func (m *Manager) RenderConfig(b Bridge, diff bool) error {
	config, live, err := m.nativeConfig(b)
	if err != nil {
		return err
	}
	if diff {
		changes := configDifferences(live, config, "")
		sort.Strings(changes)
		fmt.Fprintln(m.Out, "Diff live -> native template: paths/classes only; no values or secret hashes.")
		for _, change := range changes {
			fmt.Fprintln(m.Out, change)
		}
		fmt.Fprintf(m.Out, "%d differences; live file preserved. Not authorization to migrate.\n", len(changes))
		return nil
	}
	data, err := yaml.Marshal(Redact(config))
	if err != nil {
		return errors.New("cannot render config")
	}
	_, err = m.Out.Write(data)
	return err
}
func configDifferences(before, after any, path string) []string {
	beforeMap, beforeOK := before.(map[string]any)
	afterMap, afterOK := after.(map[string]any)
	if beforeOK && afterOK {
		keys := map[string]bool{}
		for key := range beforeMap {
			keys[key] = true
		}
		for key := range afterMap {
			keys[key] = true
		}
		var changes []string
		for key := range keys {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if path == "bridge.permissions" || path == "double_puppet.secrets" || path == "double_puppet.servers" {
				childPath = path + ".[REDACTED]"
			}
			old, oldOK := beforeMap[key]
			newValue, newOK := afterMap[key]
			if !oldOK {
				changes = append(changes, "added "+safeConfigPath(childPath))
			} else if !newOK {
				changes = append(changes, "removed "+safeConfigPath(childPath))
			} else {
				changes = append(changes, configDifferences(old, newValue, childPath)...)
			}
		}
		return changes
	}
	// YAML integers and generated overlay integers use the same decoder; slices compare as a unit.
	if !reflect.DeepEqual(before, after) {
		return []string{"changed " + safeConfigPath(path)}
	}
	return nil
}
func safeConfigPath(path string) string {
	// Unrecognized dynamic keys cannot turn a structural diff into a data dump.
	parts := strings.Split(path, ".")
	for index, part := range parts {
		if part == "[REDACTED]" {
			continue
		}
		for _, character := range part {
			if !(character >= 'a' && character <= 'z' || character == '_' || character >= '0' && character <= '9') {
				parts[index] = "[REDACTED]"
				break
			}
		}
		if len(part) > 80 {
			parts[index] = "[REDACTED]"
		}
	}
	return strings.Join(parts, ".")
}
