package chatty

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackendAmbiguousCatalogAndSymlinkInventory(t *testing.T) {
	for _, body := range []string{`{"data":[{}]}`, `{"data":[null]}`, `{"data":[{"id":"embeddinggemma-2"},{"id":"embeddinggemma-2"}]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_, _ = io.WriteString(w, body)
			}
		}))
		m, _, _ := fixture(t)
		snapshot, err := m.CollectBackendHealth(context.Background(), backendTestConfig(server.URL), "localXPC", nil)
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Components[1].Installation != "unknown" || snapshot.Components[1].Reason.Code != "invalidResponse" {
			t.Errorf("ambiguous catalog became presence/absence: %s %+v", body, snapshot.Components[1])
		}
	}
	m, _, runner := fixture(t)
	root := canonicalTempDir(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	config := backendTestConfig("")
	config.Components = []BackendProbe{{ID: "surrealdb", Binary: filepath.Join(link, "missing")}}
	snapshot, err := m.CollectBackendHealth(context.Background(), config, "localXPC", nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Components[0].Installation != "unknown" || snapshot.Components[0].Reason.Code != "invalidResponse" || len(runner.calls) != 0 {
		t.Error("unexpected ancestor symlink proved absence")
	}
}
func TestBackendCollectionValidity(t *testing.T) {
	for _, direction := range []time.Duration{-time.Minute, time.Minute} {
		m, _, _ := fixture(t)
		calls := 0
		initial := m.Now()
		m.Now = func() time.Time {
			calls++
			if calls == 1 {
				return initial
			}
			return initial.Add(direction)
		}
		snapshot, err := m.CollectBackendHealth(context.Background(), backendTestConfig(""), "localXPC", nil)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.ValidUntil.Sub(snapshot.ObservedAt) != 30*time.Second {
			t.Fatal("configured validity lost")
		}
		for _, component := range snapshot.Components {
			if component.Reason.Code != "stale" {
				t.Fatal("slow collection or reversed clock stayed current")
			}
		}
	}
}
func TestBackendConfiguredRuntimeEvidence(t *testing.T) {
	for _, test := range []struct {
		state   string
		runtime Runtime
	}{{"running", "running"}, {"waiting", "stopped"}, {"not running", "stopped"}, {"spawn scheduled", "starting"}, {"nonsense", "unknown"}} {
		m, _, runner := fixture(t)
		binary := filepath.Join(canonicalTempDir(t), "chatty-bridges")
		if err := os.WriteFile(binary, []byte("synthetic"), 0700); err != nil {
			t.Fatal(err)
		}
		runner.handle = func(_ string, args []string) ([]byte, error) {
			if args[0] == "--version" {
				return []byte("chatty-bridges version unavailable"), nil
			}
			return []byte("state = " + test.state + "\n"), nil
		}
		config := backendTestConfig("")
		config.Components = []BackendProbe{{ID: "chattyBridges", Binary: binary}}
		snapshot, err := m.CollectBackendHealth(context.Background(), config, "localXPC", nil)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Components[0].Runtime != test.runtime || snapshot.Components[0].Version.Installed != nil {
			t.Fatalf("runtime/unknown version mismatch: %+v", snapshot.Components[0])
		}
	}
}
func TestBackendFixtureUnknownFields(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(backendFixture(t), &root); err != nil {
		t.Fatal(err)
	}
	root["futureExtension"] = map[string]any{"enabled": true}
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeBackendHealth(data, backendFixtureTime()); err != nil {
		t.Fatal("unknown additive field rejected")
	}
}
