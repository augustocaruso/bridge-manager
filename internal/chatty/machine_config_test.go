package chatty

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolatedMachineHome(t *testing.T) string {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CHATTY_") {
			value := os.Getenv(key)
			t.Cleanup(func() { _ = os.Setenv(key, value) })
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	home := canonicalTempDir(t)
	t.Setenv("HOME", home)
	return home
}

func TestMachineConfigurationPrecedence(t *testing.T) {
	home := isolatedMachineHome(t)
	settings := map[string]string{
		"CHATTY_RUNTIME_ROOT":    filepath.Join(home, "custom runtime"),
		"CHATTY_SERVER_REPO":     "/synthetic/server",
		"CHATTY_STAGING_ROOT":    "/synthetic/staging",
		"CHATTY_BUILD_QUEUE":     "/synthetic/queue/submit",
		"CHATTY_LAUNCHD_PREFIX":  "org.example.chatty",
		"CHATTY_HOMESERVER":      "http://127.0.0.1:18008",
		"CHATTY_SERVER_NAME":     "matrix.example.org",
		"CHATTY_PUBLIC_BASE_URL": "https://node.example.ts.net",
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(home, "Library/Application Support/Chatty/machine.json"), string(data))
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.RuntimeRoot != settings["CHATTY_RUNTIME_ROOT"] || config.ServerRepo != settings["CHATTY_SERVER_REPO"] || config.StagingRoot != settings["CHATTY_STAGING_ROOT"] || config.QueueSubmit != settings["CHATTY_BUILD_QUEUE"] || config.LaunchdPrefix != settings["CHATTY_LAUNCHD_PREFIX"] || config.Homeserver != settings["CHATTY_HOMESERVER"] || config.ServerName != settings["CHATTY_SERVER_NAME"] || config.PublicBaseURL != settings["CHATTY_PUBLIC_BASE_URL"] {
		t.Fatalf("machine settings ignored: %+v", config)
	}
	t.Setenv("CHATTY_RUNTIME_ROOT", filepath.Join(home, "environment"))
	t.Setenv("CHATTY_LAUNCHD_PREFIX", "org.environment")
	config, err = DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.RuntimeRoot != os.Getenv("CHATTY_RUNTIME_ROOT") || config.LaunchdPrefix != "org.environment" {
		t.Fatal("environment lost precedence")
	}
	manager, _, runner := fixture(t)
	manager.Config = config
	if err := runApp(manager, "--runtime-root", filepath.Join(home, "flags"), "--launchd-prefix", "org.flags", "wellknown"); err != nil {
		t.Fatal(err)
	}
	if manager.Config.RuntimeRoot != filepath.Join(home, "flags") || manager.Config.LaunchdPrefix != "org.flags" || len(runner.calls) != 0 {
		t.Fatal("flags lost precedence")
	}
}

func TestMachineConfigurationErrors(t *testing.T) {
	home := isolatedMachineHome(t)
	path := filepath.Join(home, "machine.json")
	t.Setenv("CHATTY_MACHINE_CONFIG", path)
	if _, err := DefaultConfig(); err == nil {
		t.Fatal("explicit missing configuration accepted")
	}
	for _, data := range []string{"{", "null", "[]", `{"CHATTY_STAGING_ROOT": 42}`, `{"CHATTY_STAGING_ROOT": null}`} {
		writeFixture(t, path, data)
		if _, err := DefaultConfig(); err == nil {
			t.Fatalf("invalid config accepted: %s", data)
		}
	}
	writeFixture(t, path, `{}`)
	if _, err := DefaultConfig(); err != nil {
		t.Fatal(err)
	}
}

func TestMissingQueueSubmitBeforeEffects(t *testing.T) {
	manager, out, runner := fixture(t)
	manager.Config.QueueSubmit = ""
	if err := manager.Build(context.Background(), Bridge{Name: "instagram"}, NativeTag, "t_test"); err == nil || !strings.Contains(err.Error(), "--build-queue") {
		t.Fatalf("missing queue: %v", err)
	}
	if _, err := os.Stat(manager.Config.StagingRoot); !os.IsNotExist(err) || len(runner.calls) != 0 || out.Len() != 0 {
		t.Fatal("missing queue caused effects")
	}
}

func TestLaunchdPrefixAcrossBackendAndServices(t *testing.T) {
	for _, prefix := range []string{DefaultLaunchdPrefix, "org.example.custom"} {
		manager, _, runner := fixture(t)
		manager.Config.LaunchdPrefix = prefix
		for id, suffix := range map[ComponentID]string{"tuwunel": "tuwunel", "bridge.whatsapp": "whatsapp-bridge", "bridge.instagram": "instagram-bridge", "postgres.whatsapp": "whatsapp-postgres", "postgres.instagram": "instagram-postgres", "chattyBridges": "door"} {
			component := backendComponent(id)
			component.Installation, component.Reason.Code = "present", "none"
			before := len(runner.calls)
			manager.backendRuntime(context.Background(), &component, BackendProbe{ID: id})
			if len(runner.calls) != before+1 {
				t.Fatalf("%s: expected one launchctl call, got %v", id, runner.calls[before:])
			}
			if got := runner.calls[len(runner.calls)-1]; got != "/bin/launchctl print gui/501/"+prefix+"-"+suffix {
				t.Fatal(got)
			}
		}
		for _, bridge := range nativeDiscoveryBridges() {
			for _, component := range []string{"bridge", "postgres"} {
				if manager.serviceTarget(bridge, component) != "gui/501/"+prefix+"-"+bridge.Name+"-"+component {
					t.Fatal("service prefix ignored")
				}
			}
		}
	}
	for _, prefix := range []string{"../unsafe", "a/b", "a&b", "a\nvalue", "-leading"} {
		manager, _, runner := fixture(t)
		if err := runApp(manager, "--launchd-prefix", prefix, "service", "status", "instagram"); err == nil || len(runner.calls) != 0 {
			t.Fatal("invalid prefix reached launchctl")
		}
	}
}
