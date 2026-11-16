package chatty

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	calls   []string
	stopped map[string]bool
	handle  func(string, []string) ([]byte, error)
}

func (runner *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.calls = append(runner.calls, name+" "+strings.Join(args, " "))
	if runner.handle != nil {
		return runner.handle(name, args)
	}
	if runner.stopped == nil {
		runner.stopped = map[string]bool{}
	}
	if len(args) > 1 && args[0] == "bootout" {
		runner.stopped[args[1]] = true
	}
	if len(args) > 1 && args[0] == "print" && runner.stopped[args[1]] {
		return nil, &ProcessError{Code: 113}
	}
	return []byte("state = running\npid = 42\n"), nil
}
func fixture(t *testing.T) (*Manager, *bytes.Buffer, *fakeRunner) {
	t.Helper()
	root := canonicalTempDir(t)
	out := &bytes.Buffer{}
	runner := &fakeRunner{}
	manager := New(Config{Homeserver: "http://127.0.0.1:8008", ServerName: "example.org", RuntimeRoot: filepath.Join(root, "runtime"), ServerRepo: filepath.Join(root, "server"), StagingRoot: filepath.Join(root, "staging"), Home: root, UID: 501}, out)
	manager.Runner = runner
	manager.Config.PublicBaseURL = discoveryOrigin
	manager.Config.QueueSubmit = filepath.Join(root, "queue/submit")
	manager.Now = func() time.Time { return time.Unix(1700000000, 0) }
	manager.Wait = func(context.Context, time.Duration) error { return nil }
	writeFixture(t, filepath.Join(manager.Config.RuntimeRoot, "secrets/first-user.txt"), "username=admin\npassword='synthetic password'\nuser_id='@admin:example.org'\n")
	for _, name := range []string{"instagram", "whatsapp"} {
		bridge, _ := Lookup(name)
		writeFixture(t, manager.bridgePath(bridge, "registration.native.yaml"), fmt.Sprintf("id: %s\nurl: http://127.0.0.1:%d\nas_token: synthetic-as\nhs_token: synthetic-hs\nsender_localpart: %sbot\nnamespaces:\n  users: []\n", name, bridge.Port, name))
		writeFixture(t, manager.bridgePath(bridge, "config.native.yaml"), "bridge:\n  permissions:\n    '@synthetic-owner:example.org': admin\nappservice:\n  as_token: synthetic-as\n  hs_token: synthetic-hs\n  bot:\n    username: synthetic-bot\n  username_template: "+name+"_{{.}}\ndatabase:\n  type: postgres\n  uri: postgres://user:synthetic-db@127.0.0.1/db\nprovisioning:\n  shared_secret: synthetic-provision\ndouble_puppet:\n  secrets:\n    example.org: as_token:synthetic-puppet\nencryption:\n  pickle_key: synthetic-pickle\npublic_media:\n  signing_key: synthetic-signing\ndirect_media:\n  server_key: synthetic-server-key\n")
	}
	return manager, out, runner
}
func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func assertNoSecrets(t *testing.T, output string) {
	t.Helper()
	for _, value := range []string{"synthetic-as", "synthetic-hs", "synthetic password", "synthetic-db", "synthetic-provision", "synthetic-puppet", "synthetic-pickle", "synthetic-signing", "synthetic-server-key", "synthetic-access"} {
		if strings.Contains(output, value) {
			t.Fatalf("secret leaked (%s)", value)
		}
	}
}
func runApp(manager *Manager, args ...string) error {
	return NewApp(manager).Run(append([]string{"chatty-bridges"}, args...))
}

func TestCommandSurface(t *testing.T) {
	manager, out, _ := fixture(t)
	if err := runApp(manager, "--help"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"register", "build", "install", "config", "service", "status"} {
		if !strings.Contains(out.String(), name) {
			t.Errorf("missing %s", name)
		}
	}
	for _, name := range []string{"login-password", "proxy", "logout", "whoami", "delete"} {
		if strings.Contains(out.String(), name) {
			t.Errorf("Beeper command %s", name)
		}
	}
	for _, args := range [][]string{{"status", "../../bad"}, {"service", "start", "whatsapp"}, {"service", "start", "instagram", "extra"}, {"register"}, {"build", "instagram", "--tag", "v-other"}} {
		if err := runApp(manager, args...); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
func TestRegisterDryRunNoEffects(t *testing.T) {
	manager, out, runner := fixture(t)
	manager.HTTP = &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("dry-run performed HTTP")
		return nil, errors.New("no")
	})}
	if err := os.Remove(filepath.Join(manager.Config.RuntimeRoot, "secrets/first-user.txt")); err != nil {
		t.Fatal(err)
	}
	if err := runApp(manager, "register", "instagram", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 || !strings.Contains(out.String(), "!admin appservices register") {
		t.Fatal("bad dry-run")
	}
	assertNoSecrets(t, out.String())
}

type roundTripper func(*http.Request) (*http.Response, error)

func (transport roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func TestRegisterHTTPProtocol(t *testing.T) {
	for _, scenario := range []string{"success", "login-error", "missing-room", "send-error", "stale-confirmation", "spoofed-sender", "wrong-id", "unrelated-confirmation", "logout-error"} {
		t.Run(scenario, func(t *testing.T) {
			manager, out, _ := fixture(t)
			installDiscoveryFixture(t, manager, "instagram")
			bridge, _ := Lookup("instagram")
			if err := os.Remove(manager.bridgePath(bridge, ".registered")); err != nil {
				t.Fatal(err)
			}
			calls := []string{}
			sent := 0
			logout := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls = append(calls, request.Method+" "+request.URL.Path)
				writer.Header().Set("Content-Type", "application/json")
				if request.URL.Path != "/_matrix/client/v3/login" && request.Header.Get("Authorization") != "Bearer synthetic-access" {
					t.Error("missing auth")
				}
				switch {
				case strings.HasSuffix(request.URL.Path, "/login"):
					var body map[string]any
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body["password"] != "synthetic password" {
						t.Error("wrong credential")
					}
					if scenario == "login-error" {
						writer.WriteHeader(403)
						fmt.Fprint(writer, `{"error":"synthetic password"}`)
						return
					}
					fmt.Fprint(writer, `{"access_token":"synthetic-access"}`)
				case strings.HasSuffix(request.URL.Path, "/joined_rooms"):
					fmt.Fprint(writer, `{"joined_rooms":["!admin:example.org"]}`)
				case strings.HasSuffix(request.URL.Path, "/state/m.room.canonical_alias"):
					if scenario == "missing-room" {
						fmt.Fprint(writer, `{"alias":"#elsewhere:example.org"}`)
					} else {
						fmt.Fprint(writer, `{"alias":"#admins:example.org"}`)
					}
				case strings.Contains(request.URL.Path, "/send/"):
					sent++
					var body map[string]string
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(body["body"], "as_token: synthetic-as") || !strings.HasPrefix(body["body"], "!admin appservices register\n```yaml\n") {
						t.Error("wrong registration")
					}
					if scenario == "send-error" {
						writer.WriteHeader(500)
						fmt.Fprint(writer, `{"error":"synthetic-as"}`)
					} else {
						fmt.Fprint(writer, `{"event_id":"$sent"}`)
					}
				case strings.HasSuffix(request.URL.Path, "/messages"):
					stamp := manager.Now().UnixMilli()
					sender := "@conduit:example.org"
					if scenario == "stale-confirmation" {
						stamp--
					}
					if scenario == "spoofed-sender" {
						sender = "@other:example.org"
					}
					body := "Appservice registered with ID: instagram"
					if scenario == "wrong-id" {
						body += "-other"
					}
					boundary := `,{"event_id":"$sent"}`
					if scenario == "unrelated-confirmation" {
						boundary = ""
					}
					fmt.Fprintf(writer, `{"chunk":[{"sender":%q,"origin_server_ts":%d,"content":{"body":%q}}%s]}`, sender, stamp, body, boundary)
				case strings.HasSuffix(request.URL.Path, "/logout"):
					logout++
					if scenario == "logout-error" {
						writer.WriteHeader(500)
					}
					fmt.Fprint(writer, `{}`)
				default:
					t.Errorf("unexpected request %s", request.URL.Path)
					writer.WriteHeader(404)
				}
			}))
			defer server.Close()
			manager.Config.Homeserver = server.URL
			err := runApp(manager, "register", "instagram")
			if (err == nil) != (scenario == "success") {
				t.Fatalf("err=%v", err)
			}
			if scenario == "login-error" {
				if len(calls) != 1 || logout != 0 {
					t.Fatal("effects after failed login")
				}
			} else if logout != 1 {
				t.Fatal("session not logged out")
			}
			if scenario == "missing-room" && sent != 0 {
				t.Fatal("sent outside admin room")
			}
			if scenario == "success" || scenario == "logout-error" {
				_, document, ok := strings.Cut(out.String(), "\n")
				if !ok || !reflect.DeepEqual(discoveryEntries(t, document), []string{discoveryOrigin + "/_bridges/instagram"}) {
					t.Fatal("confirmed registration did not use discovery generator")
				}
			} else if strings.Contains(out.String(), "fi.mau.bridges") {
				t.Fatal("unconfirmed registration advertised a bridge")
			}
			info, markerErr := os.Lstat(manager.bridgePath(bridge, ".registered"))
			if scenario == "success" || scenario == "logout-error" {
				if markerErr != nil || !info.Mode().IsRegular() {
					t.Fatal("confirmed registration did not persist a regular marker")
				}
			} else if !os.IsNotExist(markerErr) {
				t.Fatal("unconfirmed registration wrote a local marker")
			}
			assertNoSecrets(t, out.String()+fmt.Sprint(err))
		})
	}
}

func TestConfigRenderAndDiff(t *testing.T) {
	manager, out, _ := fixture(t)
	if err := runApp(manager, "config", "instagram"); err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, out.String())
	for _, want := range []string{"http://127.0.0.1:8008", "example.org", "hostname: 127.0.0.1", "websocket: false"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s", want)
		}
	}
	before, err := os.ReadFile(manager.bridgePath(Bridge{Name: "instagram"}, "config.native.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runApp(manager, "config", "instagram", "--diff"); err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, out.String())
	after, err := os.ReadFile(manager.bridgePath(Bridge{Name: "instagram"}, "config.native.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("live config changed")
	}
}

func TestQueueSubmitsWithoutInstall(t *testing.T) {
	manager, out, runner := fixture(t)
	t.Setenv("CHATTY_BUILD_CARD", "t_test")
	runner.handle = func(name string, args []string) ([]byte, error) {
		if !strings.HasSuffix(name, "queue/submit") {
			t.Fatalf("build outside queue: %s", name)
		}
		return []byte("job-synthetic\n"), nil
	}
	if err := runApp(manager, "build", "instagram", "--tag", "v0.2610.0"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || !strings.Contains(runner.calls[0], "build-mautrix-native.sh instagram ") || !strings.Contains(runner.calls[0], manager.Config.StagingRoot) {
		t.Fatalf("bad build: %v", runner.calls)
	}
	if !strings.Contains(out.String(), "job-synthetic") {
		t.Fatal("job not reported")
	}
	if _, err := os.Stat(manager.binaryPath(Bridge{Name: "instagram"})); !os.IsNotExist(err) {
		t.Fatal("build installed binary")
	}
}

func TestServiceLifecycleAndWhatsAppGuard(t *testing.T) {
	for _, action := range []string{"status", "start", "stop"} {
		t.Run(action, func(t *testing.T) {
			manager, _, runner := fixture(t)
			if err := runApp(manager, "service", action, "instagram"); err != nil {
				t.Fatal(err)
			}
			if action == "start" {
				if len(runner.calls) < 2 || !strings.Contains(runner.calls[0], "instagram-postgres") {
					t.Fatal(runner.calls)
				}
			}
			if action == "stop" {
				effects := []string{}
				for _, call := range runner.calls {
					if strings.Contains(call, " bootout ") {
						effects = append(effects, call)
					}
				}
				if len(effects) != 2 || !strings.Contains(effects[0], "instagram-bridge") || !strings.Contains(effects[1], "instagram-postgres") {
					t.Fatal(runner.calls)
				}
			}
			if action == "status" {
				for _, call := range runner.calls {
					if !strings.Contains(call, " print ") {
						t.Fatal(call)
					}
				}
			}
		})
	}
	manager, _, runner := fixture(t)
	if err := runApp(manager, "service", "start", "whatsapp"); err == nil {
		t.Fatal("WhatsApp start allowed")
	}
	if len(runner.calls) != 0 {
		t.Fatal("WhatsApp process invoked")
	}
}

func TestStatusReadOnlyHTTPAndVersion(t *testing.T) {
	manager, out, runner := fixture(t)
	requests := []string{}
	manager.HTTP = &http.Client{Transport: roundTripper(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Method+" "+req.URL.Path)
		if req.Method != "GET" || req.Header.Get("Authorization") != "" {
			t.Fatal("status effect/auth")
		}
		body := `{}`
		if strings.HasSuffix(req.URL.Path, "/version") {
			body = `{"server":{"name":"Tuwunel","version":"1.9.4"}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	runner.handle = func(name string, args []string) ([]byte, error) {
		if args[0] == "--version-json" {
			return []byte(`{"FormattedVersion":"v0.2610.0","secret":"synthetic-as"}`), nil
		}
		return []byte("state = running\npid = 42\nenvironment = { secret = synthetic-as }"), nil
	}
	if err := runApp(manager, "status", "instagram"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"GET /_matrix/client/versions", "GET /_matrix/federation/v1/version", "GET /_matrix/mau/live", "GET /_matrix/mau/ready"} {
		if !slicesContains(requests, path) {
			t.Errorf("missing %s", path)
		}
	}
	if !strings.Contains(out.String(), "1.9.4") || !strings.Contains(out.String(), "v0.2610.0") {
		t.Fatal(out.String())
	}
	assertNoSecrets(t, out.String())
}
func slicesContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
