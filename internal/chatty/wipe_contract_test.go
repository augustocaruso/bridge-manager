package chatty

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWipeRoomVersion12Inventory(t *testing.T) {
	m, f := wipeFixture(t)
	room := "!abcdefghijklmnopqrstuvwxyz0123456789_-ABCDE"
	f.rooms[room] = f.rooms["!portal:example.org"]
	delete(f.rooms, "!portal:example.org")
	if err := runApp(m, "wipe", "whatsapp", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(m.Out), "portal "+room) {
		t.Fatal("v12 portal missing from inventory")
	}
	if len(f.mutations) != 0 {
		t.Fatal("dry-run mutated server")
	}
	for _, invalid := range []string{"!short", "!abcdefghijklmnopqrstuvwxyz0123456789_-ABCD?", "!abcdefghijklmnopqrstuvwxyz0123456789_-ABCD\n"} {
		if validInventoryID(invalid, '!') {
			t.Fatal("invalid room accepted")
		}
	}
	if validInventoryID("@abcdefghijklmnopqrstuvwxyz0123456789_-ABCDE", '@') {
		t.Fatal("serverless user accepted")
	}
}

func TestWipeClassification(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		members    []string
		marker     string
		membership map[string]string
		want       string
	}{
		{name: "bot-room", members: []string{"@admin:example.org", "@whatsappbot:example.org"}, want: "bot"},
		{name: "ghost-portal", members: []string{"@admin:example.org", "@whatsapp_1:example.org"}, want: "portal"},
		{name: "marker-portal", members: []string{"@admin:example.org"}, marker: "whatsapp", want: "portal"},
		{name: "mixed-marker", members: []string{"@admin:example.org", "@other:remote"}, marker: "whatsapp", want: "mixed"},
		{name: "invited-other", members: []string{"@admin:example.org", "@whatsappbot:example.org", "@other:remote"}, membership: map[string]string{"@other:remote": "invite"}, want: "mixed"},
		{name: "left-other", members: []string{"@admin:example.org", "@whatsappbot:example.org", "@other:remote"}, membership: map[string]string{"@other:remote": "leave"}, want: "bot"},
		{name: "foreign-marker", members: []string{"@admin:example.org", "@whatsappbot:example.org"}, marker: "instagram", want: "mixed"},
		{name: "owner-alone", members: []string{"@admin:example.org"}, want: "kept"},
		{name: "bot-alone-needs-review", members: []string{"@whatsappbot:example.org"}, want: "mixed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			m, f := wipeFixture(t)
			f.rooms = map[string][]string{"!case:example.org": scenario.members}
			f.markers = map[string]string{"!case:example.org": scenario.marker}
			f.membership = scenario.membership
			b, _ := Lookup("whatsapp")
			identity, err := m.wipeIdentity(b)
			if err != nil {
				t.Fatal(err)
			}
			identity.owner = "@admin:example.org"
			other, _ := Lookup("instagram")
			protected, err := m.wipeIdentity(other)
			if err != nil {
				t.Fatal(err)
			}
			kind, err := m.wipeRoomKind(context.Background(), b, identity, protected, "synthetic-access", "!case:example.org")
			if err != nil || kind != scenario.want {
				t.Fatalf("kind=%s err=%v", kind, err)
			}
		})
	}
}

func TestWipeUserRemovalIsExplicitAndRefusesMixedRooms(t *testing.T) {
	for _, scenario := range []string{"preserve", "remove", "mixed-refusal"} {
		t.Run(scenario, func(t *testing.T) {
			m, f := wipeFixture(t)
			args := []string{"wipe", "whatsapp", "--confirm", "2"}
			if scenario != "preserve" {
				args = append(args, "--remove-bridge-users")
			}
			if scenario == "remove" {
				delete(f.rooms, "!mixed:example.org")
			}
			err := runApp(m, args...)
			if scenario == "mixed-refusal" {
				if err == nil || len(f.mutations) != 0 {
					t.Fatal("mixed removal must fail before all effects", err, f.mutations)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "preserve" && len(f.deactivated) != 0 {
				t.Fatal("users deactivated by default")
			}
			if scenario == "remove" {
				if len(f.deactivated) != 3 || f.deactivated["@admin:example.org"] || f.deactivated["@instagram_1:example.org"] {
					t.Fatal("wrong user scope", f.deactivated)
				}
			}
			assertNoSecrets(t, fmt.Sprint(m.Out))
		})
	}
}

func TestWipeNamespaceOverlap(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		expression string
		wantError  string
	}{
		{name: "owner-overlap", expression: "^@(whatsapp_[0-9]+|admin):example[.]org$", wantError: "namespace includes protected user; wipe refused"},
		{name: "other-overlap", expression: "^@(whatsapp_[0-9]+|instagram_1):example[.]org$", wantError: "namespace overlaps another bridge; wipe refused"},
	} {
		for _, mode := range []string{"dry-run", "confirmed"} {
			t.Run(scenario.name+"/"+mode, func(t *testing.T) {
				m, f := wipeFixture(t)
				other, _ := Lookup("instagram")
				writeFixture(t, m.bridgePath(other, "registration.native.yaml"), "id: instagram\nurl: http://127.0.0.1:29320\nas_token: synthetic-as\nhs_token: synthetic-hs\nsender_localpart: instagrambot\nnamespaces:\n  users:\n    - exclusive: true\n      regex: '^@instagram_.*:example[.]org$'\n")
				if err := runApp(m, "wipe", "whatsapp", "--dry-run"); err != nil {
					t.Fatalf("valid baseline refused: %v", err)
				}
				if !strings.Contains(fmt.Sprint(m.Out), "--confirm 2") || len(f.mutations) != 0 {
					t.Fatal("valid baseline must inventory two rooms without effects")
				}
				bridge, _ := Lookup("whatsapp")
				registrationPath := m.bridgePath(bridge, "registration.native.yaml")
				registration, err := os.ReadFile(registrationPath)
				if err != nil {
					t.Fatal(err)
				}
				writeFixture(t, registrationPath, strings.Replace(string(registration), "^@whatsapp_[0-9]+:example[.]org$", scenario.expression, 1))
				args := []string{"wipe", "whatsapp", "--dry-run"}
				if mode == "confirmed" {
					args = []string{"wipe", "whatsapp", "--confirm", "2"}
				}
				err = runApp(m, args...)
				if err == nil || err.Error() != scenario.wantError {
					t.Fatalf("expected namespace refusal %q, got %v", scenario.wantError, err)
				}
				if len(f.mutations) != 0 {
					t.Fatal("namespace refusal mutated server", f.mutations)
				}
				for _, path := range []string{
					filepath.Join(m.Config.RuntimeRoot, "native/postgres/whatsapp/data"),
					m.bridgePath(bridge, "logs/bridge.log"),
					filepath.Join(m.Config.RuntimeRoot, "secrets/whatsapp-login-qr.png"),
				} {
					if _, err := os.Stat(path); err != nil {
						t.Fatalf("namespace refusal changed local state: %v", err)
					}
				}
				assertNoSecrets(t, fmt.Sprint(m.Out)+fmt.Sprint(err))
			})
		}
	}
}

func TestWipeExternalServerRefusedBeforeHTTP(t *testing.T) {
	for _, mode := range []string{"dry-run", "confirmed"} {
		t.Run(mode, func(t *testing.T) {
			m, f := wipeFixture(t)
			if err := runApp(m, "wipe", "whatsapp", "--dry-run"); err != nil {
				t.Fatalf("valid baseline refused: %v", err)
			}
			if len(f.requests) == 0 || len(f.mutations) != 0 || !strings.Contains(fmt.Sprint(m.Out), "--confirm 2") {
				t.Fatal("baseline did not reach read-only inventory")
			}
			f.requests = nil
			m.Config.Homeserver = "https://example.org"
			args := []string{"wipe", "whatsapp", "--dry-run"}
			if mode == "confirmed" {
				args = []string{"wipe", "whatsapp", "--confirm", "2"}
			}
			err := runApp(m, args...)
			if err == nil || err.Error() != "homeserver must be HTTP loopback without credentials, path or query" {
				t.Errorf("expected homeserver refusal, got %v", err)
			}
			if len(f.requests) != 0 || len(f.mutations) != 0 {
				t.Fatal("URL refusal reached HTTP transport", f.requests)
			}
			bridge, _ := Lookup("whatsapp")
			assertWipeLocalDataPreserved(t, m, bridge)
		})
	}
}

func TestWipeNamespaceFullMatch(t *testing.T) {
	for _, role := range []string{"target", "protected"} {
		for _, expression := range []string{"^@%s_[0-9]+:example[.]org$", "^@%s_|^@%s_[0-9]+:example[.]org$", "@%s_.*?", "^@%s_", "%s_[0-9]+:example[.]org$"} {
			t.Run(role+"/"+expression, func(t *testing.T) {
				m, f := wipeFixture(t)
				if err := runApp(m, "wipe", "whatsapp", "--dry-run"); err != nil {
					t.Fatalf("valid baseline refused: %v", err)
				}
				if !strings.Contains(fmt.Sprint(m.Out), "--confirm 2") || len(f.mutations) != 0 {
					t.Fatal("invalid baseline inventory")
				}
				bridge, _ := Lookup("whatsapp")
				if role == "protected" {
					path := m.bridgePath(bridge, "registration.native.yaml")
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					writeFixture(t, path, strings.Replace(string(data), "^@whatsapp_[0-9]+:example[.]org$", "^@(whatsapp_[0-9]+|instagram_1):example[.]org$", 1))
					bridge, _ = Lookup("instagram")
				}
				pattern := strings.ReplaceAll(expression, "%s", bridge.Name)
				writeFixture(t, m.bridgePath(bridge, "registration.native.yaml"), fmt.Sprintf("id: %s\nurl: http://127.0.0.1:%d\nas_token: synthetic-as\nhs_token: synthetic-hs\nsender_localpart: %sbot\nnamespaces:\n  users:\n    - exclusive: true\n      regex: '%s'\n", bridge.Name, bridge.Port, bridge.Name, pattern))
				whatsapp, _ := Lookup("whatsapp")
				identity, err := m.wipeIdentity(whatsapp)
				if err != nil {
					t.Fatal(err)
				}
				identity.owner = "@admin:example.org"
				plan, err := m.wipeInventory(context.Background(), whatsapp, identity, "synthetic-access")
				partial := expression == "^@%s_" || expression == "%s_[0-9]+:example[.]org$"
				if partial {
					wantUsers, wantRooms, wantMixed := 1, 1, 1
					if role == "protected" {
						wantUsers, wantRooms, wantMixed = 4, 3, 0
					}
					if err != nil || len(plan.Users) != wantUsers || len(plan.Rooms) != wantRooms || len(plan.Mixed) != wantMixed {
						t.Fatalf("partial namespace must not own full MXID: users=%d rooms=%d mixed=%d err=%v", len(plan.Users), len(plan.Rooms), len(plan.Mixed), err)
					}
				} else if role == "protected" {
					if err == nil || err.Error() != "namespace overlaps another bridge; wipe refused" {
						t.Fatalf("expected full-match overlap refusal, got %v; candidates=%d", err, len(plan.Rooms))
					}
				} else if err != nil || len(plan.Users) != 3 || len(plan.Rooms) != 2 || len(plan.Mixed) != 1 {
					t.Fatalf("full-match inventory incorrect: users=%d rooms=%d mixed=%d err=%v", len(plan.Users), len(plan.Rooms), len(plan.Mixed), err)
				}
				if len(f.mutations) != 0 {
					t.Fatal("inventory mutated HTTP state")
				}
			})
		}
	}
}

func TestWipeNamespaceAndSessionBoundaries(t *testing.T) {
	for _, scenario := range []string{"invalid-regex", "empty", "session-mismatch", "missing-session", "local-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			m, f := wipeFixture(t)
			b, _ := Lookup("whatsapp")
			registrationPath := m.bridgePath(b, "registration.native.yaml")
			registration, err := os.ReadFile(registrationPath)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "invalid-regex":
				writeFixture(t, registrationPath, strings.Replace(string(registration), "^@whatsapp_[0-9]+:example[.]org$", "[", 1))
			case "empty":
				writeFixture(t, registrationPath, strings.Split(string(registration), "namespaces:")[0]+"namespaces:\n  users: []\n")
			case "session-mismatch":
				writeFixture(t, filepath.Join(m.Config.RuntimeRoot, "secrets/matrix-admin.json"), `{"user_id":"@wrong:example.org","access_token":"synthetic-access"}`)
			case "missing-session":
				if err := os.Remove(filepath.Join(m.Config.RuntimeRoot, "secrets/matrix-admin.json")); err != nil {
					t.Fatal(err)
				}
			case "local-symlink":
				logs := m.bridgePath(b, "logs")
				if err := os.Symlink(m.bridgePath(Bridge{Name: "instagram"}, "config.native.yaml"), filepath.Join(logs, "link")); err != nil {
					t.Fatal(err)
				}
			}
			err = runApp(m, "wipe", "whatsapp", "--confirm", "2")
			if err == nil || len(f.mutations) != 0 {
				t.Fatal("unsafe boundary accepted", err, f.mutations)
			}
			assertNoSecrets(t, fmt.Sprint(m.Out)+fmt.Sprint(err))
		})
	}
}

func TestWipePaginationIsCompleteOrFailsClosed(t *testing.T) {
	for _, resource := range []string{"rooms", "users"} {
		for _, scenario := range []string{"valid", "repeat", "missing-total", "truncated", "changed-total", "duplicate", "invalid-id"} {
			t.Run(resource+"/"+scenario, func(t *testing.T) {
				m, _, _ := fixture(t)
				calls := 0
				m.HTTP = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Method != "GET" {
						t.Fatal("pagination mutation")
					}
					offset := r.URL.Query().Get("from")
					body := ""
					if resource == "rooms" {
						if offset == "0" {
							body = `{"rooms":[{"room_id":"!one:example.org"}],"offset":0,"total_rooms":2,"next_batch":1}`
						} else {
							body = `{"rooms":[{"room_id":"!two:example.org"}],"offset":1,"total_rooms":2}`
						}
						switch scenario {
						case "repeat":
							body = strings.Replace(body, `"next_batch":1`, `"next_batch":0`, 1)
						case "missing-total":
							body = strings.Replace(body, `"total_rooms":2,`, "", 1)
						case "truncated":
							body = strings.Replace(body, `,"next_batch":1`, "", 1)
						case "changed-total":
							if offset == "1" {
								body = strings.Replace(body, `"total_rooms":2`, `"total_rooms":3`, 1)
							}
						case "duplicate":
							body = strings.Replace(body, "!two:", "!one:", 1)
						case "invalid-id":
							body = strings.Replace(body, "!one:example.org", `!one\nsecret:example.org`, 1)
						}
					} else {
						if r.URL.Query().Get("deactivated") != "true" || r.URL.Query().Get("locked") != "true" {
							t.Fatal("incomplete filter")
						}
						if offset == "0" {
							body = `{"users":[{"name":"@one:example.org"}],"total":2,"next_token":"1"}`
						} else {
							body = `{"users":[{"name":"@two:example.org"}],"total":2}`
						}
						switch scenario {
						case "repeat":
							body = strings.Replace(body, `"next_token":"1"`, `"next_token":"0"`, 1)
						case "missing-total":
							body = strings.Replace(body, `"total":2,`, "", 1)
						case "truncated":
							body = strings.Replace(body, `,"next_token":"1"`, "", 1)
						case "changed-total":
							if offset == "1" {
								body = strings.Replace(body, `"total":2`, `"total":3`, 1)
							}
						case "duplicate":
							body = strings.Replace(body, "@two:", "@one:", 1)
						case "invalid-id":
							body = strings.Replace(body, "@one:example.org", `@one\nsecret:example.org`, 1)
						}
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				var err error
				if resource == "rooms" {
					var rooms []string
					rooms, err = m.wipeRooms(context.Background(), "synthetic-access")
					if scenario == "valid" && len(rooms) != 2 {
						t.Fatal(rooms)
					}
				} else {
					var users []wipeUser
					users, err = m.wipeUsers(context.Background(), "synthetic-access")
					if scenario == "valid" && len(users) != 2 {
						t.Fatal(users)
					}
				}
				if (err == nil) != (scenario == "valid") {
					t.Fatalf("err=%v", err)
				}
				if calls > 2 {
					t.Fatal("unbounded pagination")
				}
			})
		}
	}
}

func TestWipeDryRunDoesNotInspectWhatsAppDatabase(t *testing.T) {
	m, f := wipeFixture(t)
	path := filepath.Join(m.Config.RuntimeRoot, "native/postgres/whatsapp")
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent-do-not-follow", path); err != nil {
		t.Fatal(err)
	}
	if err := runApp(m, "wipe", "whatsapp", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if len(f.mutations) != 0 {
		t.Fatal(f.mutations)
	}
	if !strings.Contains(fmt.Sprint(m.Out), "not measured") {
		t.Fatal("missing forbidden database note")
	}
}
