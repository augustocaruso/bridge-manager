package chatty

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterBoundaryNoEffects(t *testing.T) {
	for _, address := range []string{"https://evil.invalid", "http://localhost:8008", "http://127.0.0.1:8008/?token=synthetic-as", "http://user:synthetic-as@127.0.0.1:8008"} {
		t.Run(address, func(t *testing.T) {
			m, out, r := fixture(t)
			m.Config.Homeserver = address
			m.HTTP = &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
				t.Fatal("unsafe request")
				return nil, errors.New("unexpected")
			})}
			if err := runApp(m, "register", "instagram"); err == nil {
				t.Fatal("unsafe address accepted")
			}
			if len(r.calls) > 0 {
				t.Fatal("process effect")
			}
			assertNoSecrets(t, out.String())
		})
	}
	m, out, _ := fixture(t)
	receiver := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect followed") }))
	defer receiver.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, receiver.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	m.Config.Homeserver = server.URL
	if err := runApp(m, "register", "instagram"); err == nil {
		t.Fatal("redirect accepted")
	}
	assertNoSecrets(t, out.String())
}
func TestInstallReceiptBackupAndIsolation(t *testing.T) {
	for _, scenario := range []string{"success", "bad-hash", "bad-tag", "outside", "source-symlink", "target-symlink", "backup-exists"} {
		t.Run(scenario, func(t *testing.T) {
			m, out, r := fixture(t)
			b, _ := Lookup("instagram")
			source := filepath.Join(m.Config.StagingRoot, "job/source")
			data := []byte("synthetic candidate")
			sum := sha256.Sum256(data)
			tag := NativeTag
			if scenario == "bad-tag" {
				tag = "wrong"
			}
			hash := fmt.Sprintf("%x", sum)
			if scenario == "bad-hash" {
				hash = strings.Repeat("0", 64)
			}
			writeFixture(t, filepath.Join(source, "mautrix-instagram"), string(data))
			writeFixture(t, filepath.Join(source, "receipt.txt"), "tag="+tag+"\nbinary (SHA-256):\n"+hash+"  mautrix-instagram\n")
			target := m.binaryPath(b)
			writeFixture(t, target, "previous binary")
			backup := target + ".pre-" + m.Now().UTC().Format("20060102T150405.000000000Z")
			if scenario == "backup-exists" {
				writeFixture(t, backup, "existing backup")
			}
			if scenario == "outside" {
				source = m.Config.RuntimeRoot
			}
			if scenario == "source-symlink" {
				link := filepath.Join(m.Config.StagingRoot, "link")
				if err := os.Symlink(source, link); err != nil {
					t.Fatal(err)
				}
				source = link
			}
			if scenario == "target-symlink" {
				other := target + ".other"
				if err := os.Rename(target, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, target); err != nil {
					t.Fatal(err)
				}
			}
			err := runApp(m, "install", "instagram", "--source", source)
			if (err == nil) != (scenario == "success") {
				t.Fatalf("scenario %s: %v", scenario, err)
			}
			current, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatal(readErr)
			}
			expected := "previous binary"
			if scenario == "success" {
				expected = string(data)
				saved, e := os.ReadFile(backup)
				if e != nil || string(saved) != "previous binary" {
					t.Fatal("backup not preserved")
				}
			}
			if string(current) != expected {
				t.Fatal("destination changed unexpectedly")
			}
			if len(r.calls) != 0 {
				t.Fatal("install executed process")
			}
			assertNoSecrets(t, out.String()+fmt.Sprint(err))
		})
	}
}
func TestBuildRejectionNoSubmission(t *testing.T) {
	for _, scenario := range []string{"tag", "card", "queue-error", "queue-output"} {
		t.Run(scenario, func(t *testing.T) {
			m, out, r := fixture(t)
			tag, card := NativeTag, "t_test"
			if scenario == "tag" {
				tag = "other"
			}
			if scenario == "card" {
				card = "bad\nvalue"
			}
			r.handle = func(string, []string) ([]byte, error) {
				if scenario == "queue-error" {
					return nil, errors.New("synthetic-as")
				}
				return []byte("synthetic-as\nnot-a-job"), nil
			}
			err := m.Build(context.Background(), Bridge{Name: "instagram"}, tag, card)
			if err == nil {
				t.Fatal("accepted invalid build")
			}
			if (scenario == "tag" || scenario == "card") && len(r.calls) != 0 {
				t.Fatal("submitted invalid build")
			}
			assertNoSecrets(t, out.String()+fmt.Sprint(err))
		})
	}
}
func TestServiceStartBootstrapAndStopVerification(t *testing.T) {
	for _, action := range []string{"start", "stop"} {
		t.Run(action, func(t *testing.T) {
			m, _, r := fixture(t)
			loaded := map[string]bool{}
			if action == "stop" {
				loaded["postgres"] = true
				loaded["bridge"] = true
			}
			for _, component := range []string{"postgres", "bridge"} {
				writeFixture(t, filepath.Join(m.Config.Home, "Library/LaunchAgents", m.Config.launchdLabel("instagram-"+component)+".plist"), "synthetic plist")
			}
			r.handle = func(_ string, args []string) ([]byte, error) {
				component := "bridge"
				if strings.Contains(strings.Join(args, " "), "-postgres") {
					component = "postgres"
				}
				switch args[0] {
				case "print":
					if !loaded[component] {
						return nil, &ProcessError{Code: 113}
					}
					return []byte("state = running\npid = 42\n"), nil
				case "bootstrap":
					loaded[component] = true
				case "bootout":
					loaded[component] = false
				default:
					t.Fatalf("unexpected %v", args)
				}
				return nil, nil
			}
			if err := runApp(m, "service", action, "instagram"); err != nil {
				t.Fatal(err)
			}
			if loaded["bridge"] != (action == "start") || loaded["postgres"] != (action == "start") {
				t.Fatal("wrong final state")
			}
			for _, component := range []string{"postgres", "bridge"} {
				found := false
				for index, call := range r.calls {
					if strings.Contains(call, " "+map[string]string{"start": "bootstrap", "stop": "bootout"}[action]+" ") && strings.Contains(call, "-"+component) {
						for _, later := range r.calls[index+1:] {
							if strings.Contains(later, " print ") && strings.Contains(later, "-"+component) {
								found = true
							}
						}
					}
				}
				if !found {
					t.Fatal("missing post-effect readback", r.calls)
				}
			}
		})
	}
}
func TestServiceFailuresClosed(t *testing.T) {
	for _, scenario := range []string{"print-error", "start-failed", "still-running", "missing-plist", "whatsapp"} {
		t.Run(scenario, func(t *testing.T) {
			m, out, r := fixture(t)
			action := "start"
			bridge := "instagram"
			if scenario == "still-running" {
				action = "stop"
			}
			if scenario == "whatsapp" {
				bridge = "whatsapp"
			}
			r.handle = func(_ string, args []string) ([]byte, error) {
				if scenario == "print-error" {
					return nil, errors.New("synthetic-as")
				}
				if scenario == "missing-plist" {
					return nil, &ProcessError{Code: 113}
				}
				if args[0] == "kickstart" {
					return nil, errors.New("synthetic-as")
				}
				if scenario == "still-running" {
					return []byte("state = running"), nil
				}
				return []byte("state = waiting"), nil
			}
			err := runApp(m, "service", action, bridge)
			if err == nil {
				t.Fatal("failure accepted")
			}
			assertNoSecrets(t, out.String()+fmt.Sprint(err))
			if scenario == "whatsapp" && len(r.calls) != 0 {
				t.Fatal("WhatsApp process")
			}
		})
	}
}
func TestStatusDegraded(t *testing.T) {
	for _, scenario := range []string{"http", "json", "process"} {
		t.Run(scenario, func(t *testing.T) {
			m, out, r := fixture(t)
			m.HTTP = &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
				body := "{}"
				code := 200
				if scenario == "http" {
					code = 503
				}
				if scenario == "json" {
					body = "synthetic-as"
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			r.handle = func(string, []string) ([]byte, error) { return nil, errors.New("synthetic-as") }
			err := runApp(m, "status", "instagram")
			if err == nil {
				t.Fatal("degraded status accepted")
			}
			assertNoSecrets(t, out.String()+fmt.Sprint(err))
		})
	}
}
func TestStatusFederationDisabledUsesInstalledVersion(t *testing.T) {
	m, out, r := fixture(t)
	m.HTTP = &http.Client{Transport: roundTripper(func(req *http.Request) (*http.Response, error) {
		code := 200
		if strings.Contains(req.URL.Path, "federation") {
			code = 403
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	r.handle = func(_ string, args []string) ([]byte, error) {
		switch args[0] {
		case "--version":
			return []byte("tuwunel 1.9.4\n"), nil
		case "--version-json":
			return []byte(`{"FormattedVersion":"v0.2610.0+dev.abcdef01","Tag":"v0.2610.0","Commit":"abcdef01","URL":"synthetic-as"}`), nil
		default:
			return []byte("state = running\npid = 42"), nil
		}
	}
	if err := runApp(m, "status", "instagram"); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"installed version", "tuwunel 1.9.4", "v0.2610.0+dev.abcdef01", "HTTP 403"} {
		if !strings.Contains(out.String(), expected) {
			t.Errorf("missing %s", expected)
		}
	}
	assertNoSecrets(t, out.String())
}

func TestConfigPreservesBotIdentity(t *testing.T) {
	m, _, _ := fixture(t)
	b, _ := Lookup("instagram")
	path := m.bridgePath(b, "config.native.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, strings.Replace(string(data), "username: synthetic-bot", "username: configured_bot", 1))
	config, _, err := m.nativeConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	if mapAt(config, "appservice", "bot")["username"] != "configured_bot" {
		t.Fatal("bot identity replaced by appservice sender localpart")
	}
}

func TestServiceInactiveStatus(t *testing.T) {
	for _, state := range []string{"waiting", "unknown", "absent"} {
		t.Run(state, func(t *testing.T) {
			m, _, r := fixture(t)
			r.handle = func(string, []string) ([]byte, error) {
				if state == "absent" {
					return nil, &ProcessError{Code: 113}
				}
				return []byte("state = " + state), nil
			}
			if err := runApp(m, "service", "status", "instagram"); err == nil {
				t.Fatal("inactive status accepted")
			}
			for _, call := range r.calls {
				if !strings.Contains(call, " print ") {
					t.Fatal("status had effects")
				}
			}
		})
	}
}

func TestServiceUnknownStateHasNoEffects(t *testing.T) {
	for _, action := range []string{"start", "stop"} {
		t.Run(action, func(t *testing.T) {
			m, _, r := fixture(t)
			r.handle = func(_ string, args []string) ([]byte, error) {
				if args[0] != "print" {
					t.Error("mutation from unknown state")
				}
				return []byte("state = unrecognized"), nil
			}
			if err := runApp(m, "service", action, "instagram"); err == nil {
				t.Fatal("unknown state accepted")
			}
		})
	}
}

func TestRedactionAndDiffDynamicKeys(t *testing.T) {
	value := map[string]any{"nested": []any{map[string]any{"signing_key": "synthetic-signing", "uri": "synthetic-db", "access_token": "synthetic-access"}}, "permissions": map[string]any{"@private:example.org": "admin"}}
	output := fmt.Sprint(Redact(value))
	assertNoSecrets(t, output)
	if strings.Contains(output, "@private") {
		t.Fatal("identity leaked")
	}
	differences := configDifferences(map[string]any{"bridge": map[string]any{"permissions": map[string]any{"@private:example.org": "admin"}}}, map[string]any{"bridge": map[string]any{"permissions": map[string]any{}}}, "")
	if strings.Contains(fmt.Sprint(differences), "private") {
		t.Fatal("diff leaked identity")
	}
}
