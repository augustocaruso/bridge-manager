package chatty

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWipeConfirmationAndHelp(t *testing.T) {
	m, out, runner := fixture(t)
	if err := runApp(m, "wipe", "whatsapp"); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("expected confirmation refusal, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("unconfirmed process")
	}
	if err := runApp(m, "wipe", "whatsapp", "--help"); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--dry-run", "--confirm", "--remove-bridge-users", "Read-only"} {
		if !strings.Contains(out.String(), flag) {
			t.Errorf("missing %s", flag)
		}
	}
}

type wipeFake struct {
	rooms               map[string][]string
	markers             map[string]string
	users               []string
	mutations           []string
	requests            []string
	failPath            string
	deletedStillPresent bool
	deactivated         map[string]bool
	erased              map[string]bool
	omitErased          bool
	eraseFails          bool
	deactivateFails     bool
	beforeRequest       func(*http.Request)
	membership          map[string]string
	deleteFails         bool
	failedKick          bool
}

func wipeFixture(t *testing.T) (*Manager, *wipeFake) {
	t.Helper()
	m, _, runner := fixture(t)
	canonicalRoot, err := filepath.EvalSymlinks(m.Config.RuntimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	m.Config.RuntimeRoot = canonicalRoot
	writeFixture(t, filepath.Join(m.Config.RuntimeRoot, "secrets/matrix-admin.json"), `{"user_id":"@admin:example.org","access_token":"synthetic-access"}`)
	b, _ := Lookup("whatsapp")
	writeFixture(t, m.bridgePath(b, "registration.native.yaml"), "id: whatsapp\nurl: http://127.0.0.1:29318\nas_token: synthetic-as\nhs_token: synthetic-hs\nsender_localpart: whatsappbot\nnamespaces:\n  users:\n    - exclusive: true\n      regex: '^@whatsapp_[0-9]+:example[.]org$'\n")
	writeFixture(t, m.bridgePath(b, "config.native.yaml"), "appservice:\n  bot:\n    username: whatsappbot\ndatabase:\n  type: postgres\n  uri: postgres://synthetic:synthetic-db@127.0.0.1:5432/whatsapp\n")
	writeFixture(t, filepath.Join(m.Config.RuntimeRoot, "native/postgres/whatsapp/data"), "old database")
	writeFixture(t, m.bridgePath(b, "logs/bridge.log"), "synthetic message content")
	writeFixture(t, filepath.Join(m.Config.RuntimeRoot, "secrets/whatsapp-login-qr.png"), "old qr")
	runner.handle = func(_ string, args []string) ([]byte, error) {
		if args[0] != "print" {
			t.Errorf("lifecycle mutation: %v", args)
		}
		return nil, &ProcessError{Code: 113}
	}
	f := &wipeFake{rooms: map[string][]string{
		"!portal:example.org": {"@admin:example.org", "@whatsappbot:example.org", "@whatsapp_1:example.org"},
		"!bot:example.org":    {"@admin:example.org", "@whatsappbot:example.org"},
		"!mixed:example.org":  {"@admin:example.org", "@whatsapp_1:example.org", "@instagram_1:example.org"},
		"!own:example.org":    {"@admin:example.org"},
	}, markers: map[string]string{"!portal:example.org": "whatsapp"}, users: []string{"@admin:example.org", "@whatsappbot:example.org", "@whatsapp_1:example.org", "@whatsapp_2:example.org", "@instagram_1:example.org"}}
	other, _ := Lookup("instagram")
	writeFixture(t, m.bridgePath(other, "registration.native.yaml"), "id: instagram\nurl: http://127.0.0.1:29320\nas_token: synthetic-as\nhs_token: synthetic-hs\nsender_localpart: instagrambot\nnamespaces:\n  users:\n    - exclusive: true\n      regex: '^@instagram_.*:example[.]org$'\n")
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.beforeRequest != nil {
			f.beforeRequest(r)
		}
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer synthetic-access" {
			t.Error("missing auth")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" {
			f.mutations = append(f.mutations, r.Method+" "+r.URL.Path)
		}
		if f.failPath != "" && strings.Contains(r.URL.Path, f.failPath) {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"error":"synthetic-access"}`)
			return
		}
		switch {
		case r.URL.Path == "/_matrix/client/v3/account/whoami":
			fmt.Fprint(w, `{"user_id":"@admin:example.org"}`)
		case r.URL.Path == "/_synapse/admin/v2/users":
			users := []map[string]any{}
			for _, id := range f.users {
				user := map[string]any{"name": id, "displayname": "private name", "admin": false, "deactivated": f.deactivated[id]}
				if !f.omitErased {
					user["erased"] = f.erased[id]
				}
				users = append(users, user)
			}
			json.NewEncoder(w).Encode(map[string]any{"users": users, "total": len(users)})
		case r.URL.Path == "/_synapse/admin/v1/rooms":
			rooms := []map[string]string{}
			for id := range f.rooms {
				rooms = append(rooms, map[string]string{"room_id": id, "name": "private room name"})
			}
			json.NewEncoder(w).Encode(map[string]any{"rooms": rooms, "total_rooms": len(rooms), "offset": 0})
		case strings.HasSuffix(r.URL.Path, "/state"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/_synapse/admin/v1/rooms/"), "/state")
			state := []map[string]any{}
			if r.URL.Query().Get("type") == "" {
				state = append(state, map[string]any{"type": "m.room.name", "state_key": "", "content": map[string]string{"name": "private room name"}})
			} else if r.URL.Query().Get("type") == "m.room.member" {
				for _, user := range f.rooms[id] {
					membership := f.membership[user]
					if membership == "" {
						membership = "join"
					}
					state = append(state, map[string]any{"type": "m.room.member", "state_key": user, "content": map[string]string{"membership": membership, "displayname": "private name"}})
				}
			} else if protocol := f.markers[id]; protocol != "" {
				state = append(state, map[string]any{"type": r.URL.Query().Get("type"), "state_key": "key", "content": map[string]any{"protocol": map[string]string{"id": protocol}}})
			}
			json.NewEncoder(w).Encode(map[string]any{"state": state})
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/_synapse/admin/v1/deactivate/"):
			var payload map[string]bool
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || !payload["erase"] {
				t.Error("missing erase")
			}
			if f.deactivated == nil {
				f.deactivated = map[string]bool{}
			}
			user := strings.TrimPrefix(r.URL.Path, "/_synapse/admin/v1/deactivate/")
			if !f.deactivateFails {
				f.deactivated[user] = true
			}
			if f.erased == nil {
				f.erased = map[string]bool{}
			}
			if !f.eraseFails {
				f.erased[user] = true
			}
			fmt.Fprint(w, `{}`)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/_synapse/admin/v1/rooms/"):
			if f.deleteFails {
				w.WriteHeader(500)
				return
			}
			if f.failedKick {
				fmt.Fprint(w, `{"failed_to_kick_users":["@whatsapp_1:example.org"]}`)
				return
			}
			var payload map[string]bool
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || !payload["purge"] || payload["block"] {
				t.Error("wrong delete payload")
			}
			if !f.deletedStillPresent {
				delete(f.rooms, strings.TrimPrefix(r.URL.Path, "/_synapse/admin/v1/rooms/"))
			}
			fmt.Fprint(w, `{"failed_to_kick_users":[]}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	})
	m.HTTP = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response.Result(), nil
	})}
	m.Config.Homeserver = "http://127.0.0.1:8008"
	return m, f
}

func TestWipeDryRunReadOnlyInventory(t *testing.T) {
	m, f := wipeFixture(t)
	if err := runApp(m, "wipe", "whatsapp", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprint(m.Out)
	for _, want := range []string{"!portal:example.org", "!bot:example.org", "!mixed:example.org", "review manually", "--confirm 2", "Bridge users kept", "not measured"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q: %s", want, text)
		}
	}
	for _, forbidden := range []string{"private room name", "private name", "synthetic message content", "@whatsapp_1"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("privacy leak %s", forbidden)
		}
	}
	if len(f.mutations) != 0 {
		t.Fatal(f.mutations)
	}
	if _, err := os.Stat(filepath.Join(m.Config.RuntimeRoot, "native/postgres/whatsapp/data")); err != nil {
		t.Fatal("database changed")
	}
	assertNoSecrets(t, text)
}

func TestWipeRealAgainstFake(t *testing.T) {
	m, f := wipeFixture(t)
	if err := runApp(m, "wipe", "whatsapp", "--confirm", "2"); err != nil {
		t.Fatal(err)
	}
	if len(f.mutations) != 2 {
		t.Fatal(f.mutations)
	}
	for _, id := range []string{"!own:example.org", "!mixed:example.org"} {
		if _, ok := f.rooms[id]; !ok {
			t.Fatal("unrelated room removed")
		}
	}
	for _, path := range []string{"native/postgres/whatsapp", "bridges/whatsapp/logs", "secrets/whatsapp-login-qr.png"} {
		if _, err := os.Lstat(filepath.Join(m.Config.RuntimeRoot, path)); !os.IsNotExist(err) {
			t.Errorf("state remains %s: %v", path, err)
		}
	}
	for _, path := range []string{"bridges/whatsapp/config.native.yaml", "bridges/whatsapp/registration.native.yaml", "bridges/instagram/config.native.yaml", "secrets/matrix-admin.json"} {
		if _, err := os.Stat(filepath.Join(m.Config.RuntimeRoot, path)); err != nil {
			t.Errorf("preserved path lost %s", path)
		}
	}
}

func assertWipeLocalDataPreserved(t *testing.T, m *Manager, bridge Bridge) {
	t.Helper()
	for _, path := range []string{
		filepath.Join(m.Config.RuntimeRoot, "native/postgres/"+bridge.Name+"/data"),
		m.bridgePath(bridge, "logs/bridge.log"),
		filepath.Join(m.Config.RuntimeRoot, "secrets/"+bridge.Name+"-login-qr.png"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("refusal changed local state: %v", err)
		}
	}
}

func TestWipeDeleteFailures(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		wantError string
	}{
		{name: "delete-error", wantError: "matrix HTTP 500"},
		{name: "failed-kick", wantError: "purge did not confirm users were kicked; cleanup stopped"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			m, f := wipeFixture(t)
			if err := runApp(m, "wipe", "whatsapp", "--dry-run"); err != nil {
				t.Fatalf("valid baseline refused: %v", err)
			}
			if !strings.Contains(fmt.Sprint(m.Out), "--confirm 2") || len(f.mutations) != 0 {
				t.Fatal("baseline must inventory two rooms without effects")
			}
			f.deleteFails = scenario.name == "delete-error"
			f.failedKick = scenario.name == "failed-kick"
			err := runApp(m, "wipe", "whatsapp", "--confirm", "2")
			if err == nil || err.Error() != scenario.wantError {
				t.Fatalf("expected delete refusal %q, got %v", scenario.wantError, err)
			}
			wantRequest := "DELETE /_synapse/admin/v1/rooms/!bot:example.org"
			if len(f.mutations) != 1 || f.mutations[0] != wantRequest || f.requests[len(f.requests)-1] != strings.Replace(wantRequest, "!", "%21", 1) {
				t.Fatal("failed delete did not stop immediately", f.mutations, f.requests)
			}
			if len(f.rooms) != 4 || len(f.deactivated) != 0 {
				t.Fatal("failure changed unrelated Matrix state")
			}
			bridge, _ := Lookup("whatsapp")
			assertWipeLocalDataPreserved(t, m, bridge)
			assertNoSecrets(t, fmt.Sprint(m.Out)+fmt.Sprint(err))
		})
	}
}

func TestWipeFailuresPreserveLocalData(t *testing.T) {
	for _, scenario := range []string{"count", "loaded", "unknown-service", "state-error", "users-error", "readback", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			m, f := wipeFixture(t)
			count := "2"
			switch scenario {
			case "count":
				count = "3"
			case "loaded":
				m.Runner = &fakeRunner{}
			case "unknown-service":
				m.Runner = &fakeRunner{handle: func(string, []string) ([]byte, error) { return nil, &ProcessError{Code: 1} }}
			case "state-error":
				f.failPath = "/state"
			case "users-error":
				f.failPath = "/users"
			case "readback":
				f.deletedStillPresent = true
			case "symlink":
				target := filepath.Join(m.Config.RuntimeRoot, "bridges/whatsapp/logs")
				if err := os.RemoveAll(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(m.Config.RuntimeRoot, "bridges/instagram"), target); err != nil {
					t.Fatal(err)
				}
			}
			err := runApp(m, "wipe", "whatsapp", "--confirm", count)
			if err == nil {
				t.Fatal("failure accepted")
			}
			if _, statErr := os.Stat(filepath.Join(m.Config.RuntimeRoot, "native/postgres/whatsapp/data")); statErr != nil {
				t.Fatal("data erased after failure")
			}
			if scenario != "readback" && len(f.mutations) > 0 {
				t.Fatal("effects before failed preflight", f.mutations)
			}
			assertNoSecrets(t, fmt.Sprint(m.Out)+fmt.Sprint(err))
		})
	}
}

func TestWipeInvalidArgumentsNoEffects(t *testing.T) {
	for _, args := range [][]string{{"wipe", "whatsapp", "--confirm", "-1"}, {"wipe", "whatsapp", "--confirm", "invalid"}, {"wipe", "whatsapp", "--dry-run", "--confirm", "0"}, {"wipe", "whatsapp", "--confirm", "0", "extra"}, {"wipe", "../../bad", "--dry-run"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			m, f := wipeFixture(t)
			if err := runApp(m, args...); err == nil {
				t.Fatal("accepted invalid args")
			}
			if len(f.requests) != 0 {
				t.Fatal(f.requests)
			}
		})
	}
}
