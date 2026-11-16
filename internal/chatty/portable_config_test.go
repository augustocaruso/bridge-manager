package chatty

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoveryExplicitOrigins(t *testing.T) {
	for _, origin := range []string{"https://example.org", "https://node.example.ts.net", "https://another.example.org"} {
		t.Run(origin, func(t *testing.T) {
			manager, out, runner := fixture(t)
			installDiscoveryFixture(t, manager, "instagram")
			if err := runApp(manager, "wellknown", "--base-url", origin); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(discoveryEntries(t, out.String()), []string{origin + "/_bridges/instagram"}) || len(runner.calls) != 0 {
				t.Fatal("explicit origin was not used without subprocesses")
			}
		})
	}
}

func TestDiscoveryRequiresExplicitOrigin(t *testing.T) {
	isolatedMachineHome(t)
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeRoot = t.TempDir()
	manager, out, runner := fixture(t)
	manager.Config = config
	path := filepath.Join(t.TempDir(), "published.json")
	writeFixture(t, path, "preserved")
	if err := runApp(manager, "wellknown", "--write", path); err == nil {
		t.Fatal("missing public origin accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "preserved" || len(runner.calls) != 0 || strings.Contains(out.String(), "fi.mau.bridges") {
		t.Fatal("missing origin caused effects")
	}
}

func TestPortableGlobalConfiguration(t *testing.T) {
	manager, out, runner := fixture(t)
	installDiscoveryFixture(t, manager, "instagram")
	runtime := filepath.Join(t.TempDir(), "custom runtime")
	if err := runApp(manager, "--runtime-root", runtime, "--server-name", "matrix.example.org", "--homeserver", "http://127.0.0.1:18008", "--server-repo", "/synthetic/server", "--staging-root", "/synthetic/staging", "--build-queue", "/synthetic/queue/submit", "--public-base-url", "https://node.example.ts.net", "wellknown"); err != nil {
		t.Fatal(err)
	}
	if manager.Config.RuntimeRoot != runtime || manager.Config.ServerName != "matrix.example.org" || manager.Config.Homeserver != "http://127.0.0.1:18008" || manager.Config.ServerRepo != "/synthetic/server" || manager.Config.StagingRoot != "/synthetic/staging" || manager.Config.QueueSubmit != "/synthetic/queue/submit" || manager.Config.PublicBaseURL != "https://node.example.ts.net" || len(runner.calls) != 0 {
		t.Fatal("global configuration ignored")
	}
	if got := discoveryEntries(t, out.String()); len(got) != 0 {
		t.Fatal("discovery read the wrong runtime")
	}
}

func TestRegistrationInvalidOriginBeforeNetwork(t *testing.T) {
	for _, origin := range []string{"", "http://example.org", "https://example.org/path", "https://user:synthetic-as@example.org"} {
		t.Run(origin, func(t *testing.T) {
			manager, _, runner := fixture(t)
			manager.Config.PublicBaseURL = origin
			manager.HTTP = &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid origin performed HTTP")
				return nil, nil
			})}
			if err := runApp(manager, "register", "instagram"); err == nil || !strings.Contains(err.Error(), "HTTPS DNS origin") {
				t.Fatalf("invalid registration origin accepted or failed for another reason: %v", err)
			}
			if len(runner.calls) != 0 {
				t.Fatal("invalid registration origin ran a process")
			}
		})
	}
}

func TestDefaultConfigurationUsesCurrentHome(t *testing.T) {
	home := isolatedMachineHome(t)
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Home != home || config.ServerName != "" || config.PublicBaseURL != "" || config.Homeserver != "http://127.0.0.1:8008" {
		t.Fatalf("non-neutral defaults: %+v", config)
	}
	for _, path := range []string{config.RuntimeRoot, config.ServerRepo, config.StagingRoot} {
		if !strings.HasPrefix(path, home+string(filepath.Separator)) {
			t.Fatalf("default path outside current home: %q", path)
		}
	}
	if config.ServerRepo != filepath.Join(home, "Library/Application Support/Chatty/matrix-server") || config.RuntimeRoot != filepath.Join(home, "Library/Application Support/Chatty/runtime") || config.StagingRoot != filepath.Join(home, "Library/Caches/Chatty/bridge-manager") || config.QueueSubmit != "" || config.LaunchdPrefix != "local.chatty" {
		t.Fatal("server checkout default is not portable")
	}
}

func TestDiscoveryLocalFlagOverridesGlobalOrigin(t *testing.T) {
	manager, out, _ := fixture(t)
	installDiscoveryFixture(t, manager, "instagram")
	if err := runApp(manager, "--public-base-url", "https://global.example.org", "wellknown", "--base-url", "https://local.example.org"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(discoveryEntries(t, out.String()), []string{"https://local.example.org/_bridges/instagram"}) {
		t.Fatal("local base-url did not override global origin")
	}
}
