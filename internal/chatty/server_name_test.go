package chatty

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerNameRequiredBeforeEffects(t *testing.T) {
	bridge, _ := Lookup("instagram")
	for _, test := range []struct {
		name string
		run  func(*Manager) error
	}{
		{"config", func(m *Manager) error { return m.RenderConfig(bridge, false) }},
		{"config-diff", func(m *Manager) error { return m.RenderConfig(bridge, true) }},
		{"register-plan", func(m *Manager) error { return m.Register(context.Background(), bridge, true) }},
		{"register", func(m *Manager) error { return m.Register(context.Background(), bridge, false) }},
		{"wipe-inventory", func(m *Manager) error { return m.Wipe(context.Background(), bridge, WipeOptions{DryRun: true}) }},
		{"wipe", func(m *Manager) error { return m.Wipe(context.Background(), bridge, WipeOptions{Confirm: "0"}) }},
		{"health", func(m *Manager) error {
			_, err := m.CollectBackendHealth(context.Background(), backendTestConfig("http://127.0.0.1:18009"), "localXPC", nil)
			return err
		}},
	} {
		for _, missing := range []string{"", " \t"} {
			t.Run(test.name+"/"+missing, func(t *testing.T) {
				manager, out, runner := fixture(t)
				manager.Config.ServerName = missing
				manager.HTTP.Transport = roundTripper(func(*http.Request) (*http.Response, error) {
					t.Fatal("missing server name performed HTTP")
					return nil, nil
				})
				if err := test.run(manager); err == nil || !strings.Contains(err.Error(), "--server-name") {
					t.Fatalf("missing identity did not fail clearly: %v", err)
				}
				if len(runner.calls) != 0 || out.Len() != 0 {
					t.Fatalf("missing identity caused effects: %v %s", runner.calls, out)
				}
			})
		}
	}
}

func TestHealthCommandsRequireServerNameBeforeReadingConfig(t *testing.T) {
	for _, args := range [][]string{
		{"health", "--backend-config", "/synthetic/missing-health.json", "--json"},
		{"door", "--listen", "127.0.0.1:8448", "--hostname", "node.example.ts.net", "--cert-dir", "/synthetic/certs", "--wellknown-file", "/synthetic/discovery.json", "--backend-config", "/synthetic/missing-health.json"},
	} {
		t.Run(args[0], func(t *testing.T) {
			manager, _, runner := fixture(t)
			manager.Config.ServerName = ""
			if err := runApp(manager, args...); err == nil || !strings.Contains(err.Error(), "--server-name") {
				t.Fatalf("missing identity not rejected at command admission: %v", err)
			}
			if len(runner.calls) != 0 {
				t.Fatal("missing identity reached a subprocess")
			}
		})
	}
}

func TestIdentityIndependentDiscoveryNeedsNoServerName(t *testing.T) {
	manager, out, runner := fixture(t)
	manager.Config.ServerName = ""
	installDiscoveryFixture(t, manager, "instagram")
	path := filepath.Join(t.TempDir(), "discovery.json")
	if err := runApp(manager, "wellknown", "--base-url", discoveryOrigin, "--write", path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), discoveryOrigin+"/_bridges/instagram") || out.Len() != 0 || len(runner.calls) != 0 {
		t.Fatal("discovery acquired an unrelated server-name requirement")
	}
}
