package chatty

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

func replaceWipeNamespace(t *testing.T, m *Manager, name, expression string) {
	t.Helper()
	bridge, _ := Lookup(name)
	path := m.bridgePath(bridge, "registration.native.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, strings.Split(string(data), "namespaces:")[0]+expression)
}

func TestWipeProtectedNamespaceRequired(t *testing.T) {
	for _, mode := range []string{"dry-run", "confirmed"} {
		t.Run(mode, func(t *testing.T) {
			m, f := wipeFixture(t)
			if err := runApp(m, "wipe", "whatsapp", "--dry-run"); err != nil {
				t.Fatal("valid baseline", err)
			}
			if !strings.Contains(fmt.Sprint(m.Out), "--confirm 2") {
				t.Fatal("baseline count")
			}
			replaceWipeNamespace(t, m, "instagram", "namespaces:\n  users: []\n")
			args := []string{"wipe", "whatsapp", "--dry-run"}
			if mode == "confirmed" {
				args = []string{"wipe", "whatsapp", "--confirm", "2"}
			}
			err := runApp(m, args...)
			if err == nil || err.Error() != "invalid other bridge namespace" {
				t.Errorf("expected protected namespace refusal, got %v", err)
			}
			if len(f.mutations) != 0 {
				t.Error("namespace refusal mutated server", f.mutations)
			}
			bridge, _ := Lookup("whatsapp")
			assertWipeLocalDataPreserved(t, m, bridge)
		})
	}
}

func TestWipeProtectedMemberOutsideUserInventory(t *testing.T) {
	for _, membership := range []string{"join", "invite", "leave"} {
		for _, mode := range []string{"dry-run", "confirmed"} {
			t.Run(membership+"/"+mode, func(t *testing.T) {
				m, f := wipeFixture(t)
				f.membership = map[string]string{"@instagram_1:example.org": membership}
				if err := runApp(m, "wipe", "whatsapp", "--dry-run"); err != nil {
					t.Fatal("valid baseline", err)
				}
				replaceWipeNamespace(t, m, "whatsapp", "namespaces:\n  users:\n    - exclusive: true\n      regex: '^@(whatsapp_[0-9]+|instagram_1):example[.]org$'\n")
				if err := runApp(m, "wipe", "whatsapp", "--dry-run"); err == nil || err.Error() != "namespace overlaps another bridge; wipe refused" {
					t.Fatal("overlap control", err)
				}
				f.users = f.users[:len(f.users)-1]
				args := []string{"wipe", "whatsapp", "--dry-run"}
				if mode == "confirmed" {
					args = []string{"wipe", "whatsapp", "--confirm", "3"}
				}
				err := runApp(m, args...)
				if membership == "leave" {
					if err != nil {
						t.Fatal("historical member control", err)
					}
					return
				}
				if err == nil || err.Error() != "namespace overlaps another bridge; wipe refused" {
					t.Errorf("expected active member overlap refusal, got %v", err)
				}
				if len(f.mutations) != 0 {
					t.Error("inventory refusal mutated server", f.mutations)
				}
				bridge, _ := Lookup("whatsapp")
				assertWipeLocalDataPreserved(t, m, bridge)
			})
		}
	}
}

func TestWipeProtectedMemberPreDelete(t *testing.T) {
	for _, membership := range []string{"join", "invite", "leave"} {
		t.Run(membership, func(t *testing.T) {
			m, f := wipeFixture(t)
			delete(f.rooms, "!bot:example.org")
			delete(f.rooms, "!mixed:example.org")
			replaceWipeNamespace(t, m, "whatsapp", "namespaces:\n  users:\n    - exclusive: true\n      regex: '^@(whatsapp_[0-9]+|instagram_9):example[.]org$'\n")
			if err := runApp(m, "wipe", "whatsapp", "--dry-run"); err != nil {
				t.Fatal("valid baseline", err)
			}
			if !strings.Contains(fmt.Sprint(m.Out), "--confirm 1") {
				t.Fatal("baseline count")
			}
			calls := 0
			f.membership = map[string]string{"@instagram_9:example.org": membership}
			f.beforeRequest = func(r *http.Request) {
				if r.URL.Path == "/_synapse/admin/v1/rooms/!portal:example.org/state" && r.URL.Query().Get("type") == "m.room.member" {
					calls++
					if calls == 2 {
						f.rooms["!portal:example.org"] = append(f.rooms["!portal:example.org"], "@instagram_9:example.org")
					}
				}
			}
			err := runApp(m, "wipe", "whatsapp", "--confirm", "1")
			if calls != 2 {
				t.Fatal("pre-delete state not reached", calls)
			}
			if membership == "leave" {
				if err != nil || len(f.mutations) != 1 {
					t.Fatal("historical member control", err, f.mutations)
				}
				return
			}
			if err == nil || err.Error() != "namespace overlaps another bridge; wipe refused" {
				t.Errorf("expected pre-delete overlap refusal, got %v", err)
			}
			if len(f.mutations) != 0 {
				t.Error("revalidation deleted protected room", f.mutations)
			}
			bridge, _ := Lookup("whatsapp")
			assertWipeLocalDataPreserved(t, m, bridge)
		})
	}
}

func TestWipeEraseState(t *testing.T) {
	for _, scenario := range []string{"active", "deactivated", "erased-only", "already-erased", "erase-not-applied", "erase-field-absent", "deactivation-not-applied", "preserve"} {
		t.Run(scenario, func(t *testing.T) {
			m, f := wipeFixture(t)
			delete(f.rooms, "!mixed:example.org")
			user := "@whatsapp_1:example.org"
			f.users = []string{"@admin:example.org", user, "@instagram_1:example.org"}
			f.deactivated = map[string]bool{user: scenario != "active" && scenario != "deactivation-not-applied" && scenario != "erased-only"}
			f.erased = map[string]bool{user: scenario == "already-erased" || scenario == "erased-only"}
			if err := runApp(m, "wipe", "whatsapp", "--dry-run", "--remove-bridge-users"); err != nil {
				t.Fatal("valid baseline", err)
			}
			if len(f.mutations) != 0 {
				t.Fatal("dry-run effects")
			}
			f.eraseFails = scenario == "erase-not-applied"
			f.omitErased = scenario == "erase-field-absent"
			f.deactivateFails = scenario == "deactivation-not-applied"
			args := []string{"wipe", "whatsapp", "--confirm", "2"}
			if scenario != "preserve" {
				args = append(args, "--remove-bridge-users")
			}
			err := runApp(m, args...)
			refusal := f.eraseFails || f.omitErased || f.deactivateFails
			if refusal {
				if err == nil || err.Error() != "deactivation and erase unconfirmed; local state preserved" {
					t.Errorf("expected erase readback refusal, got %v", err)
				}
				bridge, _ := Lookup("whatsapp")
				assertWipeLocalDataPreserved(t, m, bridge)
				if f.requests[len(f.requests)-1] != "GET /_synapse/admin/v2/users?from=0&limit=100&deactivated=true&locked=true" {
					t.Error("did not stop at erase readback")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			posts := 0
			for _, mutation := range f.mutations {
				if strings.HasPrefix(mutation, "POST ") {
					posts++
					if mutation != "POST /_synapse/admin/v1/deactivate/"+user {
						t.Error("wrong user erased", mutation)
					}
				}
			}
			wantPosts := 1
			if scenario == "already-erased" || scenario == "preserve" {
				wantPosts = 0
			}
			if posts != wantPosts {
				t.Errorf("erase requests=%d want=%d", posts, wantPosts)
			}
			if !refusal && wantPosts == 1 && (!f.deactivated[user] || !f.erased[user]) {
				t.Error("erase not applied")
			}
			if scenario == "preserve" && f.erased[user] {
				t.Error("default erased ghost")
			}
		})
	}
}
