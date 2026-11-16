package chatty

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBackendProcessPermissionsAndSanitization(t *testing.T) {
	binary := filepath.Join(canonicalTempDir(t), "not-executable")
	if err := os.WriteFile(binary, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := (ExecRunner{}).Run(context.Background(), binary, "--version")
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("lost process permission classification: %v", err)
	}
	m, _, runner := fixture(t)
	for _, id := range []ComponentID{"chattyBridges", "tailscale", "postgres.whatsapp"} {
		config := backendTestConfig("")
		config.Components = []BackendProbe{{ID: id, Binary: binary, Cluster: canonicalTempDir(t), Host: "127.0.0.1", Port: 55432}}
		snapshot, err := m.CollectBackendHealth(context.Background(), config, "localXPC", nil)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Components[0].Installation != "present" || snapshot.Components[0].Reason.Code != "permissionDenied" || len(runner.calls) != 0 {
			t.Fatal("non-executable binary was executed or called absent")
		}
	}
	if err := os.Chmod(binary, 0700); err != nil {
		t.Fatal(err)
	}
	runner.handle = func(_ string, args []string) ([]byte, error) {
		if args[0] == "--version" {
			return []byte("chatty-bridges version 0.1.0"), nil
		}
		return nil, os.ErrPermission
	}
	for _, id := range []ComponentID{"chattyBridges", "tailscale", "postgres.whatsapp"} {
		config := backendTestConfig("")
		config.Components = []BackendProbe{{ID: id, Binary: binary, Cluster: canonicalTempDir(t), Host: "127.0.0.1", Port: 55432}}
		snapshot, err := m.CollectBackendHealth(context.Background(), config, "localXPC", nil)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Components[0].Reason.Code != "permissionDenied" || snapshot.Components[0].Authorization != "denied" || snapshot.Components[0].Runtime != "unknown" {
			t.Errorf("process denial became other state: %+v", snapshot.Components[0])
		}
	}
}
func TestBackendInstalledVersionIdentity(t *testing.T) {
	for _, test := range []struct {
		id           ComponentID
		output, want string
	}{
		{"tuwunel", "tuwunel 1.9.4\n", "1.9.4"},
		{"postgres.whatsapp", "pg_isready (PostgreSQL) 17.11", "17.11"},
		{"surrealdb", "surreal 3.3.4 for macos on aarch64", "3.3.4"},
		{"chattyBridges", "chatty-bridges version 0.1.0", "0.1.0"},
		{"localai", "local-ai version v4.12.1", "4.12.1"},
		{"tuwunel", "error 12", ""},
		{"tuwunel", "tuwunel 1.9.4\nprivate output 1.2", ""},
		{"tuwunel", "tuwunel /private/1.9.4", ""},
	} {
		got := installedVersionOutput(test.id, []byte(test.output))
		if test.want == "" {
			if got != nil {
				t.Fatalf("invalid version accepted: %q", test.output)
			}
		} else if got == nil || *got != test.want {
			t.Fatalf("version %q => %v", test.output, got)
		}
	}
}
