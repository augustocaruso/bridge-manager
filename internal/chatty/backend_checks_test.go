package chatty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func backendFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/backend-v1/healthy-unknown-version.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func backendFixtureTime() time.Time { return time.Date(2026, 10, 30, 21, 0, 1, 0, time.UTC) }
func TestBackendFixtureMutations(t *testing.T) {
	type mutation struct {
		Name   string
		Path   []string
		Value  any
		Remove bool
		Error  string
	}
	data, err := os.ReadFile("testdata/backend-v1/mutations.json")
	if err != nil {
		t.Fatal(err)
	}
	var mutations []mutation
	if err := json.Unmarshal(data, &mutations); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeBackendHealth(backendFixture(t), backendFixtureTime()); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range mutations {
		t.Run(mutation.Name, func(t *testing.T) {
			var root any
			if err := json.Unmarshal(backendFixture(t), &root); err != nil {
				t.Fatal(err)
			}
			parent := root
			for _, key := range mutation.Path[:len(mutation.Path)-1] {
				switch node := parent.(type) {
				case map[string]any:
					parent = node[key]
				case []any:
					index, err := strconv.Atoi(key)
					if err != nil {
						t.Fatal(err)
					}
					parent = node[index]
				}
			}
			object := parent.(map[string]any)
			key := mutation.Path[len(mutation.Path)-1]
			if mutation.Remove {
				delete(object, key)
			} else {
				object[key] = mutation.Value
			}
			changed, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := DecodeBackendHealth(changed, backendFixtureTime())
			if mutation.Error != "" {
				if err == nil || err.Error() != mutation.Error {
					t.Fatalf("got %v want %s", err, mutation.Error)
				}
			} else if err != nil {
				t.Fatal(err)
			} else if snapshot.Components[0].Runtime != "running" {
				t.Fatal("version or future reason downgraded runtime")
			}
		})
	}
}
func TestBackendFreshnessAndClock(t *testing.T) {
	for _, now := range []time.Time{backendFixtureTime().Add(-2 * time.Second), backendFixtureTime().Add(29 * time.Second), backendFixtureTime().Add(time.Hour)} {
		snapshot, err := DecodeBackendHealth(backendFixture(t), now)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Components[0].Reason.Code != "stale" {
			t.Fatal("expired or incoherent clock stayed healthy")
		}
	}
	snapshot, err := DecodeBackendHealth(backendFixture(t), backendFixtureTime())
	if err != nil {
		t.Fatal(err)
	}
	checked := snapshot.ObservedAt.Add(-time.Hour)
	expired := snapshot.ObservedAt.Add(-time.Second)
	snapshot.Components[0].Functionality = "passed"
	snapshot.Components[0].FunctionalCheckedAt = &checked
	snapshot.Components[0].FunctionalValidUntil = &expired
	snapshot.Expire(backendFixtureTime())
	if snapshot.Components[0].Functionality != "unknown" || snapshot.Components[0].Reason.Code != "stale" || !snapshot.Components[0].FunctionalCheckedAt.Equal(checked) {
		t.Fatal("readiness renewed functional proof")
	}
}
func backendTestConfig(origin string) BackendConfig {
	return BackendConfig{InstallationID: "11111111-1111-4111-8111-111111111111", ValiditySeconds: 30, Readers: []string{"@reader:synthetic.example"}, Components: []BackendProbe{{ID: "localai", Origin: origin}, {ID: "model.embeddinggemma2", Origin: origin, Token: "synthetic-hub"}}}
}
func TestBackendChecksHTTPOutcomes(t *testing.T) {
	for _, test := range []struct {
		name         string
		status       int
		body         string
		code         ReasonCode
		installation Installation
	}{
		{"model-listed", 200, `{"data":[{"id":"embeddinggemma-2"}]}`, "none", "present"},
		{"catalogue-without-model", 200, `{"data":[]}`, "modelUnavailable", "absent"},
		{"catalogue-401", 401, `{}`, "credentialRequired", "unknown"},
		{"catalogue-403", 403, `{}`, "unauthorized", "unknown"},
		{"catalogue-404", 404, `{}`, "invalidResponse", "unknown"},
		{"catalogue-malformed", 200, `{}`, "invalidResponse", "unknown"},
		{"catalogue-null", 200, `{"data":null}`, "invalidResponse", "unknown"},
		{"catalogue-redirect", 302, `{}`, "invalidResponse", "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.URL.Path == "/readyz" {
					w.WriteHeader(200)
					return
				}
				if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer synthetic-hub" {
					t.Error("unexpected endpoint or credential")
				}
				w.Header().Set("Location", "http://foreign.invalid")
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			m, _, runner := fixture(t)
			snapshot, err := m.CollectBackendHealth(context.Background(), backendTestConfig(server.URL), "localXPC", nil)
			if err != nil {
				t.Fatal(err)
			}
			model := snapshot.Components[1]
			if model.Reason.Code != test.code || model.Installation != test.installation || model.Runtime != "unknown" || model.Functionality != "notTested" {
				t.Fatalf("bad model: %+v", model)
			}
			if len(calls) != 2 || len(runner.calls) != 0 {
				t.Fatalf("unexpected effects: %v %v", calls, runner.calls)
			}
		})
	}
}
func TestBackendInventoryAndUnreachable(t *testing.T) {
	for _, test := range []struct {
		name         string
		failure      error
		installation Installation
		code         ReasonCode
	}{
		{"proven-absence", os.ErrNotExist, "absent", "notInstalled"}, {"permission-denied", os.ErrPermission, "unknown", "permissionDenied"}, {"unknown-io", errors.New("synthetic"), "unknown", "unknownState"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, _, runner := fixture(t)
			m.Lstat = func(string) (os.FileInfo, error) { return nil, test.failure }
			config := backendTestConfig("")
			config.Components = []BackendProbe{{ID: "surrealdb", Binary: "/synthetic/bin"}}
			snapshot, err := m.CollectBackendHealth(context.Background(), config, "localXPC", nil)
			if err != nil {
				t.Fatal(err)
			}
			component := snapshot.Components[0]
			if component.Installation != test.installation || component.Reason.Code != test.code || component.Runtime != "unknown" || len(runner.calls) != 0 {
				t.Fatalf("bad inventory: %+v", component)
			}
		})
	}
	m, _, _ := fixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	config := backendTestConfig(origin)
	snapshot, err := m.CollectBackendHealth(context.Background(), config, "localXPC", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range snapshot.Components {
		if component.Reason.Code != "unreachable" || component.Runtime != "unknown" || component.Installation != "unknown" {
			t.Fatalf("unreachable became absence/stopped: %+v", component)
		}
	}
}
func TestBackendTuwunelFederationAndUnknownVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_matrix/client/versions" {
			_, _ = fmt.Fprint(w, `{"versions":["v1.16"]}`)
		} else {
			w.WriteHeader(403)
		}
	}))
	defer server.Close()
	m, _, _ := fixture(t)
	config := backendTestConfig("")
	config.Components = []BackendProbe{{ID: "tuwunel", Origin: server.URL}}
	snapshot, err := m.CollectBackendHealth(context.Background(), config, "localXPC", nil)
	if err != nil {
		t.Fatal(err)
	}
	component := snapshot.Components[0]
	if component.Reason.Code != "none" || component.Runtime != "running" || component.Version.Installed != nil || component.Version.Running != nil {
		t.Fatalf("unknown version or federation blocked service: %+v", component)
	}
}
func TestBackendDependenciesAndPostgresClusters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_matrix/client/versions" {
			_, _ = fmt.Fprint(w, `{"versions":["v1.16"]}`)
		} else if r.URL.Path == "/_matrix/federation/v1/version" {
			w.WriteHeader(403)
		}
	}))
	defer server.Close()
	m, _, runner := fixture(t)
	binary := filepath.Join(canonicalTempDir(t), "pg_isready")
	if err := os.WriteFile(binary, []byte("synthetic"), 0700); err != nil {
		t.Fatal(err)
	}
	cluster := canonicalTempDir(t)
	runner.handle = func(name string, args []string) ([]byte, error) {
		if args[0] == "--version" {
			return []byte("pg_isready (PostgreSQL) 17.11"), nil
		}
		if args[0] == "print" {
			return nil, &ProcessError{Code: 113}
		}
		if strings.Contains(strings.Join(args, " "), "55432") {
			return nil, &ProcessError{Code: 2}
		}
		return nil, nil
	}
	config := backendTestConfig("")
	config.Components = []BackendProbe{{ID: "tuwunel", Origin: server.URL}, {ID: "postgres.whatsapp", Binary: binary, Cluster: cluster, Host: "127.0.0.1", Port: 55432}, {ID: "postgres.instagram", Binary: binary, Cluster: cluster, Host: "127.0.0.1", Port: 55433}, {ID: "bridge.whatsapp", Origin: server.URL}, {ID: "bridge.instagram", Origin: server.URL}}
	snapshot, err := m.CollectBackendHealth(context.Background(), config, "localXPC", nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Components[1].Reason.Code != "unreachable" || snapshot.Components[2].Reason.Code != "none" {
		t.Fatal("clusters not independent")
	}
	if snapshot.Components[3].Reason.Code != "dependencyUnavailable" || snapshot.Components[3].Reason.DependencyID == nil || *snapshot.Components[3].Reason.DependencyID != "postgres.whatsapp" || snapshot.Components[4].Reason.Code != "none" {
		t.Fatal("dependency not identified")
	}
	if snapshot.Components[1].Version.Installed == nil || *snapshot.Components[1].Version.Installed != "17.11" {
		t.Fatal("installed version not separate")
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "start") || strings.Contains(call, "stop") {
			t.Fatal("lifecycle effect")
		}
	}
}
func TestBackendLimits(t *testing.T) {
	baseline, err := DecodeBackendHealth(backendFixture(t), backendFixtureTime())
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BackendHealth){
		func(s *BackendHealth) { s.Components = append(s.Components, make([]ComponentHealth, 64)...) },
		func(s *BackendHealth) { s.Components[0].DependsOn = make([]ComponentID, 65) },
		func(s *BackendHealth) { s.MatrixServerName = strings.Repeat("a", 1025) },
		func(s *BackendHealth) {
			version := strings.Repeat("1", 129)
			s.Components[0].Version.Installed = &version
		},
	} {
		data, _ := json.Marshal(baseline)
		var snapshot BackendHealth
		if err := json.Unmarshal(data, &snapshot); err != nil {
			t.Fatal(err)
		}
		mutate(&snapshot)
		data, _ = json.Marshal(snapshot)
		if _, err := DecodeBackendHealth(data, backendFixtureTime()); err == nil {
			t.Fatal("limit accepted")
		}
	}
	if _, err := DecodeBackendHealth(append(backendFixture(t), []byte(strings.Repeat(" ", backendMaxBytes))...), backendFixtureTime()); err == nil {
		t.Fatal("oversize envelope accepted")
	}
	for _, data := range [][]byte{backendFixture(t)[:30], append(backendFixture(t), []byte(`{}`)...)} {
		if _, err := DecodeBackendHealth(data, backendFixtureTime()); err == nil {
			t.Fatal("truncated/trailing document accepted")
		}
	}
}
