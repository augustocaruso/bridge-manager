package chatty

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRegistrationDiscoveryContinuity(t *testing.T) {
	for _, name := range []string{"instagram", "whatsapp"} {
		for _, state := range []string{"absent", "present", "symlink", "directory", "missing-parent"} {
			t.Run(name+"/"+state, func(t *testing.T) {
				manager, out, runner := fixture(t)
				manager.Config.PublicBaseURL = "https://registered.example.org"
				installDiscoveryFixture(t, manager, name)
				bridge, _ := Lookup(name)
				marker := manager.bridgePath(bridge, ".registered")
				original, err := os.Stat(marker)
				if err != nil {
					t.Fatal(err)
				}
				if state != "present" {
					if err := os.Remove(marker); err != nil {
						t.Fatal(err)
					}
				}
				before := map[string][]byte{}
				for _, file := range []string{"config.native.yaml", "registration.native.yaml"} {
					before[file], err = os.ReadFile(manager.bridgePath(bridge, file))
					if err != nil {
						t.Fatal(err)
					}
				}
				target := filepath.Join(t.TempDir(), "preserved")
				writeFixture(t, target, "untouched")
				sent, logout := 0, 0
				manager.HTTP = &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
					body := "{}"
					switch {
					case strings.HasSuffix(request.URL.Path, "/login"):
						body = `{"access_token":"synthetic-access"}`
					case strings.HasSuffix(request.URL.Path, "/joined_rooms"):
						body = `{"joined_rooms":["!admin:example.org"]}`
					case strings.HasSuffix(request.URL.Path, "/state/m.room.canonical_alias"):
						body = `{"alias":"#admins:example.org"}`
					case strings.Contains(request.URL.Path, "/send/"):
						sent++
						if state != "present" {
							if _, err := os.Lstat(marker); !os.IsNotExist(err) {
								t.Fatal("marker written before confirmation")
							}
						}
						body = `{"event_id":"$sent"}`
					case strings.HasSuffix(request.URL.Path, "/messages"):
						switch state {
						case "symlink":
							err = os.Symlink(target, marker)
						case "directory":
							err = os.Mkdir(marker, 0700)
						case "missing-parent":
							err = os.Rename(filepath.Dir(marker), filepath.Dir(marker)+"-saved")
						}
						if err != nil {
							t.Fatal(err)
						}
						body = fmt.Sprintf(`{"chunk":[{"sender":"@conduit:example.org","origin_server_ts":%d,"content":{"body":%q}},{"event_id":"$sent"}]}`, manager.Now().UnixMilli(), "Appservice registered with ID: "+name)
					case strings.HasSuffix(request.URL.Path, "/logout"):
						logout++
					default:
						t.Fatalf("unexpected request: %s", request.URL.Path)
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
				})}
				err = runApp(manager, "register", name)
				if sent != 1 || logout != 1 || len(runner.calls) != 0 {
					t.Fatal("unexpected registration effects")
				}
				if state == "absent" || state == "present" {
					if err != nil {
						t.Fatal(err)
					}
					_, announced, _ := strings.Cut(out.String(), "\n")
					published := filepath.Join(t.TempDir(), "mautrix.json")
					out.Reset()
					if err := runApp(manager, "wellknown", "--write", published); err != nil {
						t.Fatal(err)
					}
					data, err := os.ReadFile(published)
					if err != nil {
						t.Fatal(err)
					}
					want := []string{manager.Config.PublicBaseURL + "/_bridges/" + name}
					if !reflect.DeepEqual(discoveryEntries(t, string(data)), want) || string(data) != announced {
						t.Fatalf("confirmed bridge lost between register and publication: %s", data)
					}
					info, err := os.Lstat(marker)
					if err != nil || !info.Mode().IsRegular() {
						t.Fatal("confirmation marker missing or not regular")
					}
					if state == "present" {
						data, err := os.ReadFile(marker)
						if err != nil || string(data) != "synthetic registration marker" || !os.SameFile(original, info) || info.Mode() != original.Mode() {
							t.Fatal("existing marker changed")
						}
					}
				} else if err == nil || !strings.Contains(err.Error(), "registration confirmed") || !strings.Contains(err.Error(), "marker") || !strings.Contains(err.Error(), "do not repeat") {
					t.Fatalf("missing partial-success guidance: %v", err)
				}
				assertNoSecrets(t, out.String()+fmt.Sprint(err))
				for file, want := range before {
					path := manager.bridgePath(bridge, file)
					if state == "missing-parent" {
						path = filepath.Join(filepath.Dir(marker)+"-saved", file)
					}
					data, err := os.ReadFile(path)
					if err != nil || string(data) != string(want) {
						t.Fatal("native configuration changed")
					}
				}
				data, err := os.ReadFile(target)
				if err != nil || string(data) != "untouched" {
					t.Fatal("marker symlink target changed")
				}
				dir := filepath.Dir(marker)
				if state == "missing-parent" {
					dir += "-saved"
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".chatty-registration-") {
						t.Fatal("registration temporary leaked")
					}
				}
			})
		}
	}
}

func TestRegisterDryRunPreservesMarkers(t *testing.T) {
	for _, name := range []string{"instagram", "whatsapp"} {
		for _, present := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/present=%t", name, present), func(t *testing.T) {
				manager, _, runner := fixture(t)
				bridge, _ := Lookup(name)
				marker := manager.bridgePath(bridge, ".registered")
				if present {
					writeFixture(t, marker, "preserved")
				}
				manager.HTTP = &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
					t.Fatal("dry-run performed HTTP")
					return nil, nil
				})}
				if err := runApp(manager, "register", name, "--dry-run"); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(marker)
				if present && (err != nil || string(data) != "preserved") || !present && !os.IsNotExist(err) || len(runner.calls) != 0 {
					t.Fatal("dry-run changed marker or ran a process")
				}
			})
		}
	}
}
