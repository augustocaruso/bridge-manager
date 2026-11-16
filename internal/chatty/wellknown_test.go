package chatty

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const discoveryOrigin = "https://node.example.ts.net"

func installDiscoveryFixture(t *testing.T, m *Manager, name string) {
	t.Helper()
	b, err := Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, m.binaryPath(b), "synthetic executable")
	if err := os.Chmod(m.binaryPath(b), 0700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, m.bridgePath(b, ".registered"), "synthetic registration marker")
}

func discoveryEntries(t *testing.T, data string) []string {
	t.Helper()
	var result map[string][]string
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		t.Fatalf("invalid discovery JSON: %v: %s", err, data)
	}
	entries, ok := result["fi.mau.bridges"]
	if !ok || entries == nil || len(result) != 1 {
		t.Fatalf("unexpected discovery document: %s", data)
	}
	return entries
}

func TestWellKnownInventory(t *testing.T) {
	for _, names := range [][]string{nil, {"instagram"}, {"whatsapp"}, {"whatsapp", "instagram"}} {
		t.Run(fmt.Sprint(names), func(t *testing.T) {
			m, out, runner := fixture(t)
			m.HTTP = &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) { t.Fatal("discovery performed HTTP"); return nil, nil })}
			for _, name := range names {
				installDiscoveryFixture(t, m, name)
			}
			if err := runApp(m, "wellknown"); err != nil {
				t.Fatal(err)
			}
			want := []string{}
			for _, name := range []string{"instagram", "whatsapp"} {
				for _, installed := range names {
					if installed == name {
						want = append(want, discoveryOrigin+"/_bridges/"+name)
					}
				}
			}
			if got := discoveryEntries(t, out.String()); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v want %v", got, want)
			}
			first := out.String()
			out.Reset()
			if err := runApp(m, "wellknown", "--base-url", discoveryOrigin+"/"); err != nil {
				t.Fatal(err)
			}
			if out.String() != first || len(runner.calls) != 0 {
				t.Fatal("unstable output or process executed")
			}
			assertNoSecrets(t, out.String())
		})
	}
}

func TestWellKnownRequiresInstalledAndRegistered(t *testing.T) {
	for _, missing := range []string{"binary", "executable", "config.native.yaml", "registration.native.yaml", ".registered"} {
		t.Run(missing, func(t *testing.T) {
			m, out, _ := fixture(t)
			installDiscoveryFixture(t, m, "instagram")
			b, _ := Lookup("instagram")
			path := m.bridgePath(b, missing)
			if missing == "binary" || missing == "executable" {
				path = m.binaryPath(b)
			}
			if missing == "executable" {
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := runApp(m, "wellknown"); err != nil {
				t.Fatal(err)
			}
			if got := discoveryEntries(t, out.String()); len(got) != 0 {
				t.Fatalf("advertised incomplete bridge: %v", got)
			}
		})
	}
}

func TestWellKnownRejectsUnsafeBaseBeforeWrite(t *testing.T) {
	for _, base := range []string{"http://node.example.ts.net", "https://-invalid.example", "https://node.example.ts.net:evil.example", discoveryOrigin + ":443", discoveryOrigin + "/path", discoveryOrigin + "?secret=synthetic-as", discoveryOrigin + "#fragment", discoveryOrigin + "?", discoveryOrigin + "#", "https://user:synthetic-as@node.example.ts.net", "//node.example.ts.net", ""} {
		t.Run(base, func(t *testing.T) {
			m, out, runner := fixture(t)
			path := filepath.Join(t.TempDir(), "mautrix.json")
			writeFixture(t, path, "old")
			err := runApp(m, "wellknown", "--base-url", base, "--write", path)
			if err == nil {
				t.Fatal("accepted unsafe base")
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || string(data) != "old" || len(runner.calls) != 0 {
				t.Fatal("effects after invalid base")
			}
			assertNoSecrets(t, out.String()+err.Error())
		})
	}
}

func TestWellKnownAtomicWrite(t *testing.T) {
	m, out, _ := fixture(t)
	installDiscoveryFixture(t, m, "instagram")
	dir := t.TempDir()
	path := filepath.Join(dir, "mautrix.json")
	writeFixture(t, path, "old")
	old, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := runApp(m, "wellknown", "--write", path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := discoveryEntries(t, string(data)); !reflect.DeepEqual(got, []string{discoveryOrigin + "/_bridges/instagram"}) {
		t.Fatal(got)
	}
	oldData := make([]byte, 3)
	if _, err := old.Read(oldData); err != nil || string(oldData) != "old" {
		t.Fatal("file was truncated in place")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || out.Len() != 0 {
		t.Fatal("write touched another file or printed non-JSON output")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := runApp(m, "wellknown", "--write", path); err != nil {
		t.Fatal(err)
	}
}

func TestWellKnownWriteRefusesSpecialTargetsAndCleansUp(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "missing-parent"} {
		t.Run(kind, func(t *testing.T) {
			m, _, _ := fixture(t)
			dir := t.TempDir()
			target := filepath.Join(dir, "target")
			path := filepath.Join(dir, "mautrix.json")
			writeFixture(t, target, "preserved")
			switch kind {
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "missing-parent":
				path = filepath.Join(dir, "missing", "mautrix.json")
			}
			before, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := runApp(m, "wellknown", "--write", path); err == nil {
				t.Fatal("accepted unsafe target")
			}
			after, err := os.ReadDir(dir)
			if err != nil || len(after) != len(before) {
				t.Fatal("temporary file leaked")
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "preserved" {
				t.Fatal("target changed")
			}
		})
	}
}

func TestWellKnownInvalidRegistrationPreservesPublishedFile(t *testing.T) {
	for _, content := range []string{"id: whatsapp\nurl: http://127.0.0.1:29320\n", "id: instagram\nurl: https://foreign.example\n", "[invalid"} {
		m, out, _ := fixture(t)
		installDiscoveryFixture(t, m, "instagram")
		b, _ := Lookup("instagram")
		writeFixture(t, m.bridgePath(b, "registration.native.yaml"), content)
		path := filepath.Join(t.TempDir(), "mautrix.json")
		writeFixture(t, path, "old")
		if err := runApp(m, "wellknown", "--write", path); err == nil {
			t.Fatal("accepted invalid registration")
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "old" || strings.Contains(out.String(), "foreign.example") {
			t.Fatal("invalid inventory affected publication")
		}
	}
}
