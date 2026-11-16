package chatty

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

type bridgeWellKnown struct {
	Bridges []string `json:"fi.mau.bridges"`
}

func wellKnownCommand(m *Manager) *cli.Command {
	return &cli.Command{
		Name: "wellknown", Usage: "Generate mautrix discovery without network access; print or atomically write a file",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "base-url", Usage: "Explicit public HTTPS homeserver origin; overrides --public-base-url"},
			&cli.StringFlag{Name: "write", Usage: "Publish only to this file; default: print JSON"},
		},
		Action: func(ctx *cli.Context) error {
			if ctx.NArg() != 0 {
				return errors.New("extra arguments are not allowed")
			}
			if ctx.IsSet("write") && ctx.String("write") == "" {
				return errors.New("--write requires a nonempty path")
			}
			base := m.Config.PublicBaseURL
			if ctx.IsSet("base-url") {
				base = ctx.String("base-url")
			}
			data, err := m.wellKnown(base)
			if err != nil {
				return err
			}
			if ctx.IsSet("write") {
				return writeWellKnown(ctx.String("write"), data)
			}
			_, err = m.Out.Write(data)
			return err
		},
	}
}

func validateDiscoveryBase(address string) (string, error) {
	// The operator explicitly selects the trusted origin; there is no machine-specific
	// allowlist. CHATTY.md documents this boundary; an unset origin disables publication.
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || !validDNSHostname(parsed.Host) || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" || strings.ContainsAny(address, "?#") || parsed.Opaque != "" {
		return "", errors.New("configure --public-base-url or --base-url with an HTTPS DNS origin, without port, credentials, path, query or fragment")
	}
	return strings.TrimRight(address, "/"), nil
}

func discoveryFile(path string, executable bool) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("cannot inspect discovery inventory")
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("discovery inventory requires regular files without symlinks")
	}
	return !executable || info.Mode().Perm()&0111 != 0, nil
}

func (m *Manager) wellKnown(baseURL string) ([]byte, error) {
	base, err := validateDiscoveryBase(baseURL)
	if err != nil {
		return nil, err
	}
	bridges, err := m.registeredBridges()
	if err != nil {
		return nil, err
	}
	document := bridgeWellKnown{Bridges: []string{}}
	for _, bridge := range bridges {
		document.Bridges = append(document.Bridges, base+"/_bridges/"+bridge.Name)
	}
	data, err := json.Marshal(document)
	if err != nil {
		return nil, errors.New("discovery serialization failed")
	}
	return append(data, '\n'), nil
}

func nativeDiscoveryBridges() []Bridge {
	var bridges []Bridge
	for _, name := range []string{"instagram", "whatsapp"} {
		bridge, _ := Lookup(name)
		bridges = append(bridges, bridge)
	}
	return bridges
}

func (m *Manager) registeredBridges() ([]Bridge, error) {
	var bridges []Bridge
	for _, bridge := range nativeDiscoveryBridges() {
		installed, err := discoveryFile(m.binaryPath(bridge), true)
		if err != nil {
			return nil, err
		}
		if !installed {
			continue
		}
		complete := true
		files := []string{"config.native.yaml", "registration.native.yaml", ".registered"}
		for _, file := range files {
			present, err := discoveryFile(m.bridgePath(bridge, file), false)
			if err != nil {
				return nil, err
			}
			if !present {
				complete = false
			}
		}
		if !complete {
			continue
		}
		data, err := os.ReadFile(m.bridgePath(bridge, "registration.native.yaml"))
		if err != nil {
			return nil, errors.New("discovery registration unavailable")
		}
		var identity struct {
			ID  string `yaml:"id"`
			URL string `yaml:"url"`
		}
		if yaml.Unmarshal(data, &identity) != nil || identity.ID != bridge.Name || identity.URL != fmt.Sprintf("http://127.0.0.1:%d", bridge.Port) {
			return nil, errors.New("invalid discovery registration for native bridge")
		}
		bridges = append(bridges, bridge)
	}
	return bridges, nil
}

func writeWellKnown(path string, data []byte) error {
	info, err := os.Lstat(path)
	mode := os.FileMode(0644)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("discovery destination must be a regular file without symlink")
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return errors.New("discovery destination unavailable")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".chatty-wellknown-*")
	if err != nil {
		return errors.New("cannot create discovery temporary file in destination directory")
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Chmod(mode)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot write discovery; destination preserved")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return errors.New("cannot publish discovery atomically")
	}
	return nil
}
