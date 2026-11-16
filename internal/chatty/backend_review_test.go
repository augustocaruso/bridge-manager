package chatty

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackendPostgresReadinessEvidence(t *testing.T) {
	for _, id := range []ComponentID{"postgres.whatsapp", "postgres.instagram"} {
		for _, test := range []struct {
			name          string
			failure       error
			runtime       Runtime
			reachability  Reachability
			authorization Authorization
			reason        ReasonCode
		}{
			{"accepting", nil, "running", "reachable", "notApplicable", "none"},
			{"rejecting", &ProcessError{Code: 1}, "starting", "reachable", "granted", "starting"},
			{"no-response", &ProcessError{Code: 2}, "unknown", "unreachable", "granted", "unreachable"},
			{"no-attempt", &ProcessError{Code: 3}, "unknown", "unknown", "granted", "unknownState"},
			{"permission", os.ErrPermission, "unknown", "unknown", "denied", "permissionDenied"},
			{"other-error", errors.New("synthetic"), "unknown", "unknown", "granted", "unknownState"},
		} {
			t.Run(string(id)+"/"+test.name, func(t *testing.T) {
				m, _, runner := fixture(t)
				binary := filepath.Join(canonicalTempDir(t), "pg_isready")
				if err := os.WriteFile(binary, []byte("synthetic"), 0700); err != nil {
					t.Fatal(err)
				}
				runner.handle = func(_ string, args []string) ([]byte, error) {
					switch args[0] {
					case "--version":
						return []byte("pg_isready (PostgreSQL) 17.11"), nil
					case "print":
						return []byte("state = unrecognized\n"), nil
					default:
						return nil, test.failure
					}
				}
				config := backendTestConfig("")
				config.Components = []BackendProbe{{ID: id, Binary: binary, Cluster: canonicalTempDir(t), Host: "127.0.0.1", Port: 55432}}
				snapshot, err := m.CollectBackendHealth(context.Background(), config, "localXPC", nil)
				if err != nil {
					t.Fatal(err)
				}
				got := snapshot.Components[0]
				if got.Runtime != test.runtime || got.Reachability != test.reachability || got.Authorization != test.authorization || got.Reason.Code != test.reason || got.CheckKind != "readiness" || got.Installation != "present" || got.Functionality != "notTested" || got.CheckedAt == nil || !got.CheckedAt.Equal(m.Now()) {
					t.Fatalf("lost readiness evidence: %+v", got)
				}
			})
		}
	}
}

func TestBackendRuntimeCheckProvenance(t *testing.T) {
	for _, test := range []struct {
		name, output  string
		failure       error
		runtime       Runtime
		reachability  Reachability
		authorization Authorization
		reason        ReasonCode
	}{
		{"Running", `{"BackendState":"Running"}`, nil, "running", "reachable", "granted", "none"},
		{"Stopped", `{"BackendState":"Stopped"}`, nil, "stopped", "unknown", "granted", "none"},
		{"Starting", `{"BackendState":"Starting"}`, nil, "starting", "unknown", "granted", "starting"},
		{"NeedsLogin", `{"BackendState":"NeedsLogin"}`, nil, "unknown", "unknown", "required", "credentialRequired"},
		{"NeedsMachineAuth", `{"BackendState":"NeedsMachineAuth"}`, nil, "unknown", "unknown", "granted", "unknownState"},
		{"NoState", `{"BackendState":"NoState"}`, nil, "unknown", "unknown", "granted", "unknownState"},
		{"InUseOtherUser", `{"BackendState":"InUseOtherUser"}`, nil, "unknown", "unknown", "granted", "unknownState"},
		{"future", `{"BackendState":"FutureState"}`, nil, "unknown", "unknown", "granted", "unknownState"},
		{"empty", `{}`, nil, "unknown", "unknown", "granted", "unknownState"},
		{"malformed", `{`, nil, "unknown", "unknown", "granted", "invalidResponse"},
		{"permission", "", os.ErrPermission, "unknown", "unknown", "denied", "permissionDenied"},
		{"failure", "", &ProcessError{Code: 1}, "unknown", "unknown", "granted", "unknownState"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, _, runner := fixture(t)
			component := backendComponent("tailscale")
			component.Installation, component.Authorization, component.Reason.Code = "present", "granted", "none"
			component.CheckKind = "inventory"
			inventoryTime := m.Now().UTC()
			component.CheckedAt = &inventoryTime
			checkedAt := inventoryTime.Add(time.Second)
			runner.handle = func(_ string, args []string) ([]byte, error) {
				if fmt.Sprint(args) != "[status --json]" {
					t.Fatalf("unexpected command: %v", args)
				}
				m.Now = func() time.Time { return checkedAt }
				return []byte(test.output), test.failure
			}
			m.backendRuntime(context.Background(), &component, BackendProbe{ID: "tailscale", Binary: "/synthetic/tailscale"})
			if component.Runtime != test.runtime || component.Reachability != test.reachability || component.Authorization != test.authorization || component.Reason.Code != test.reason || component.CheckKind != "service" || component.CheckedAt == nil || !component.CheckedAt.Equal(checkedAt) {
				t.Fatalf("lost status evidence: %+v", component)
			}
		})
	}
	for _, id := range []ComponentID{"tuwunel", "bridge.whatsapp", "bridge.instagram", "postgres.whatsapp", "postgres.instagram", "chattyBridges"} {
		for _, failure := range []error{nil, os.ErrPermission, errors.New("synthetic")} {
			t.Run(string(id)+"/"+fmt.Sprint(failure), func(t *testing.T) {
				m, _, runner := fixture(t)
				component := backendComponent(id)
				component.Installation, component.Reason.Code = "present", "none"
				runner.handle = func(_ string, _ []string) ([]byte, error) { return []byte("state = running\n"), failure }
				m.backendRuntime(context.Background(), &component, BackendProbe{ID: id})
				if component.CheckKind != "service" || component.CheckedAt == nil || !component.CheckedAt.Equal(m.Now()) {
					t.Fatalf("supervisor check misrepresented: %+v", component)
				}
			})
		}
	}
}
