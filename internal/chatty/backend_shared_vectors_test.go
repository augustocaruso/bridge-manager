package chatty

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestBackendSharedScenarioVectors(t *testing.T) {
	for _, name := range []string{"stale", "model-missing", "changed-origin", "changed-installation"} {
		t.Run(name, func(t *testing.T) {
			baseline, err := DecodeBackendHealth(backendFixture(t), backendFixtureTime())
			if err != nil {
				t.Fatal(err)
			}
			expected := baseline
			switch name {
			case "stale":
				expected.ValidUntil = expected.ObservedAt.Add(500 * time.Millisecond)
			case "model-missing":
				expected.Components[1].Installation = "absent"
				expected.Components[1].Reason = HealthReason{Code: "modelUnavailable"}
			case "changed-origin":
				origin := "https://other.synthetic.example"
				expected.SourceOrigin = &origin
			case "changed-installation":
				expected.InstallationID = "33333333-3333-4333-8333-333333333333"
			}
			data, err := os.ReadFile("testdata/backend-v1/" + name + ".json")
			if err != nil {
				t.Fatalf("missing shared %s vector: %v", name, err)
			}
			var raw BackendHealth
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(raw, expected) {
				t.Fatalf("%s must change only its stated observation, got %+v want %+v", name, raw, expected)
			}
			snapshot, err := DecodeBackendHealth(data, backendFixtureTime())
			if err != nil {
				t.Fatal(err)
			}
			expected.Expire(backendFixtureTime())
			if !reflect.DeepEqual(snapshot, expected) {
				t.Fatalf("wrong decoded %s observation: %+v", name, snapshot)
			}
			if name == "stale" {
				for _, now := range []time.Time{raw.ObservedAt.Add(-time.Second), raw.ValidUntil, raw.ValidUntil.Add(time.Second)} {
					stale, err := DecodeBackendHealth(data, now)
					if err != nil {
						t.Fatal(err)
					}
					for _, component := range stale.Components {
						if component.Reason.Code != "stale" {
							t.Fatal("incoherent or expired observation stayed current")
						}
					}
				}
				fresh, err := DecodeBackendHealth(data, raw.ObservedAt)
				if err != nil || fresh.Components[0].Reason.Code != "none" {
					t.Fatal("validity boundary lost its fresh control")
				}
			}
			if name == "model-missing" {
				catalog, err := os.ReadFile("testdata/backend-v1/catalog-without-model.json")
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" {
						t.Error("health attempted mutation")
					}
					switch r.URL.Path {
					case "/readyz":
					case "/v1/models":
						if r.Header.Get("Authorization") != "Bearer synthetic-hub" {
							t.Error("catalog missing own credential")
						}
						_, _ = w.Write(catalog)
					default:
						t.Errorf("unexpected request: %s", r.URL.Path)
						w.WriteHeader(404)
					}
				}))
				defer server.Close()
				m, _, runner := fixture(t)
				m.Now = func() time.Time { return baseline.ObservedAt }
				collected, err := m.CollectBackendHealth(context.Background(), backendTestConfig(server.URL), "tailnetHTTPS", baseline.SourceOrigin)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(collected.Components[1], snapshot.Components[1]) || collected.Components[0].Reason.Code != "none" || len(runner.calls) != 0 {
					t.Fatalf("catalog projection differs from shared vector: %+v", collected)
				}
			}
		})
	}
}

func TestBackendHTTPResponseReachabilityFamily(t *testing.T) {
	for _, id := range []ComponentID{"tuwunel", "bridge.whatsapp", "bridge.instagram", "surrealdb", "localai", "model.embeddinggemma2"} {
		for _, status := range []int{401, 403, 404, 503} {
			m, _, _ := fixture(t)
			m.HTTP.Transport = roundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: io.NopCloser(http.NoBody)}, nil
			})
			component := m.collectBackendComponent(context.Background(), BackendProbe{ID: id, Origin: "http://127.0.0.1:1"})
			if component.Reachability != "reachable" || component.Functionality != "notTested" || component.Installation != "unknown" {
				t.Errorf("%s HTTP %d discarded response or invented health: %+v", id, status, component)
			}
		}
	}
}
