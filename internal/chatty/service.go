package chatty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ServiceState struct {
	Loaded   bool
	State    string
	PID      int
	ExitCode int
}

func (m *Manager) serviceTarget(bridge Bridge, component string) string {
	return fmt.Sprintf("gui/%d/%s", m.Config.UID, m.Config.launchdLabel(bridge.Name+"-"+component))
}
func (m *Manager) serviceState(ctx context.Context, b Bridge, component string) (ServiceState, error) {
	return m.serviceStateTarget(ctx, m.serviceTarget(b, component))
}
func (m *Manager) serviceStateTarget(ctx context.Context, target string) (ServiceState, error) {
	output, err := m.Runner.Run(ctx, "/bin/launchctl", "print", target)
	if err != nil {
		var processErr *ProcessError
		if errors.As(err, &processErr) && processErr.Code == 113 {
			return ServiceState{State: "not_loaded"}, nil
		}
		return ServiceState{}, &observationError{message: "launchctl print unavailable; state unknown", cause: err}
	}
	state := ServiceState{Loaded: true, State: "unknown"}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok {
			continue
		}
		switch key {
		case "state":
			if value == "running" || value == "waiting" || value == "not running" || value == "spawn scheduled" {
				state.State = value
			}
		case "pid":
			state.PID, _ = strconv.Atoi(value)
		case "last exit code":
			state.ExitCode, _ = strconv.Atoi(value)
		}
	}
	if state.State == "unknown" {
		return state, errors.New("unknown LaunchAgent state")
	}
	return state, nil
}
func (m *Manager) printState(b Bridge, component string, state ServiceState) {
	fmt.Fprintf(m.Out, "%s/%s: loaded=%t state=%s pid=%d last_exit=%d\n", b.Name, component, state.Loaded, state.State, state.PID, state.ExitCode)
}
func (m *Manager) Service(ctx context.Context, b Bridge, action string) error {
	if action != "status" && action != "start" && action != "stop" {
		return errors.New("invalid service action")
	}
	if b.Name == "whatsapp" && action != "status" {
		return errors.New("WhatsApp lifecycle blocked by policy; no process executed")
	}
	components := []string{"postgres", "bridge"}
	if action == "stop" {
		components = []string{"bridge", "postgres"}
	}
	var failures []error
	for _, component := range components {
		target := m.serviceTarget(b, component)
		if action == "status" {
			state, err := m.serviceState(ctx, b, component)
			m.printState(b, component, state)
			if err != nil {
				failures = append(failures, err)
			} else if !state.Loaded || state.State != "running" {
				failures = append(failures, errors.New("LaunchAgent is not running"))
			}
			continue
		}
		state, err := m.serviceState(ctx, b, component)
		if err != nil {
			return err
		}
		if action == "start" {
			if !state.Loaded {
				plist := filepath.Join(m.Config.Home, "Library/LaunchAgents", m.Config.launchdLabel(b.Name+"-"+component)+".plist")
				info, err := os.Lstat(plist)
				if err != nil || !info.Mode().IsRegular() {
					return errors.New("existing plist not found or not regular")
				}
				if _, err = m.Runner.Run(ctx, "/bin/launchctl", "bootstrap", fmt.Sprintf("gui/%d", m.Config.UID), plist); err != nil {
					return errors.New("launchctl bootstrap failed")
				}
				state, err = m.serviceState(ctx, b, component)
				if err != nil {
					return err
				}
			}
			if state.State != "running" {
				if _, err = m.Runner.Run(ctx, "/bin/launchctl", "kickstart", target); err != nil {
					return errors.New("launchctl kickstart failed")
				}
			}
			if err = m.awaitService(ctx, b, component, true); err != nil {
				return err
			}
			state, err = m.serviceState(ctx, b, component)
			if err != nil {
				return err
			}
			if !state.Loaded || state.State != "running" {
				return errors.New("LaunchAgent left the running state during verification")
			}
			m.printState(b, component, state)
		} else {
			if state.Loaded {
				if _, err = m.Runner.Run(ctx, "/bin/launchctl", "bootout", target); err != nil {
					return errors.New("launchctl bootout failed; state unconfirmed")
				}
			}
			if err = m.awaitService(ctx, b, component, false); err != nil {
				return err
			}
		}
	}
	return errors.Join(failures...)
}

func (m *Manager) awaitService(ctx context.Context, b Bridge, component string, running bool) error {
	for attempt := 0; attempt < 30; attempt++ {
		state, err := m.serviceState(ctx, b, component)
		if err != nil {
			return err
		}
		if running && state.Loaded && state.State == "running" || !running && !state.Loaded {
			return nil
		}
		if err = m.Wait(ctx, time.Second); err != nil {
			return err
		}
	}
	return errors.New("LaunchAgent state unconfirmed after lifecycle action")
}

func validateHomeserver(address string) error {
	parsed, err := url.Parse(address)
	if err != nil || parsed.User != nil || parsed.Scheme != "http" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("homeserver must be HTTP loopback without credentials, path or query")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return errors.New("homeserver must use a literal loopback IP")
	}
	return nil
}
func (m *Manager) probe(ctx context.Context, address string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return errors.New("invalid health URL")
	}
	client := *m.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("endpoint unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if result != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result); err != nil {
			return errors.New("invalid version JSON")
		}
	}
	return nil
}
func safeVersion(value string) string {
	if value == "" || len(value) > 120 {
		return "unknown"
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune(".-_+ /()", character)) {
			return "unknown"
		}
	}
	return value
}
func (m *Manager) Status(ctx context.Context, b Bridge) error {
	if err := validateHomeserver(m.Config.Homeserver); err != nil {
		return err
	}
	var failures []error
	if err := m.Service(ctx, b, "status"); err != nil {
		failures = append(failures, err)
	}
	var versions struct {
		Versions []string `json:"versions"`
	}
	if err := m.probe(ctx, m.Config.Homeserver+"/_matrix/client/versions", &versions); err != nil {
		fmt.Fprintln(m.Out, "Tuwunel client: unavailable")
		failures = append(failures, err)
	} else {
		fmt.Fprintln(m.Out, "Tuwunel client: HTTP 200")
	}
	var server struct {
		Server struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"server"`
	}
	if err := m.probe(ctx, m.Config.Homeserver+"/_matrix/federation/v1/version", &server); err != nil {
		fmt.Fprintf(m.Out, "Tuwunel version via HTTP: unavailable (%s)\n", err)
		output, versionErr := m.Runner.Run(ctx, filepath.Join(m.Config.RuntimeRoot, "native/bin/tuwunel"), "--version")
		installed := strings.TrimSpace(string(output))
		if versionErr != nil || !strings.HasPrefix(installed, "tuwunel ") || safeVersion(installed) == "unknown" {
			fmt.Fprintln(m.Out, "Tuwunel installed version: unavailable")
			failures = append(failures, errors.New("Tuwunel version unavailable via HTTP and local binary"))
		} else {
			fmt.Fprintf(m.Out, "Tuwunel installed version: %s (does not prove the running process version)\n", installed)
		}
	} else {
		fmt.Fprintf(m.Out, "Tuwunel version: %s %s\n", safeVersion(server.Server.Name), safeVersion(server.Server.Version))
	}
	for _, endpoint := range []string{"live", "ready"} {
		err := m.probe(ctx, fmt.Sprintf("http://127.0.0.1:%d/_matrix/mau/%s", b.Port, endpoint), nil)
		if err != nil {
			fmt.Fprintf(m.Out, "%s %s: unavailable\n", b.Name, endpoint)
			failures = append(failures, err)
		} else {
			fmt.Fprintf(m.Out, "%s %s: HTTP 200\n", b.Name, endpoint)
		}
	}
	output, err := m.Runner.Run(ctx, m.binaryPath(b), "--version-json")
	if err != nil {
		fmt.Fprintln(m.Out, "Bridge version: unavailable")
		failures = append(failures, errors.New("version binary unavailable"))
	} else {
		var version struct {
			FormattedVersion string
			Tag              string `json:"tag"`
			Commit           string `json:"commit"`
		}
		if json.Unmarshal(output, &version) != nil {
			failures = append(failures, errors.New("invalid binary version JSON"))
		} else {
			fmt.Fprintf(m.Out, "%s version: %s tag=%s commit=%s (%s)\n", b.Name, safeVersion(version.FormattedVersion), safeVersion(version.Tag), safeVersion(version.Commit), filepath.Base(m.binaryPath(b)))
		}
	}
	return errors.Join(failures...)
}
