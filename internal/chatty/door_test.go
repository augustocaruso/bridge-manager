package chatty

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoorCommandContract(t *testing.T) {
	m, out, runner := fixture(t)
	if err := runApp(m, "door", "--help"); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--listen", "--hostname", "--cert-dir", "--matrix-upstream", "--wellknown-file", "--wait-timeout", "--tailscale-bin"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatalf("door help missing %s", flag)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatal("help executed a process")
	}
}

type doorEcho struct{ Target, Path, RawPath, Query, Host, ForwardedFor, Proto, Authorization, Cookie, Body string }

func TestDoorRouting(t *testing.T) {
	m, _, _ := fixture(t)
	for _, name := range []string{"instagram", "whatsapp"} {
		installDiscoveryFixture(t, m, name)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(doorEcho{Target: r.Header.Get("Test-Target"), Path: r.URL.Path, RawPath: r.URL.EscapedPath(), Query: r.URL.RawQuery, Host: r.Host, ForwardedFor: r.Header.Get("X-Forwarded-For"), Proto: r.Header.Get("X-Forwarded-Proto"), Authorization: r.Header.Get("Authorization"), Cookie: r.Header.Get("Cookie"), Body: string(body)})
	}))
	defer upstream.Close()
	fake, _ := url.Parse(upstream.URL)
	m.HTTP.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		r.Header.Set("Test-Target", r.URL.Host)
		r.URL.Host = fake.Host
		return http.DefaultTransport.RoundTrip(r)
	})
	matrix, _ := url.Parse(upstream.URL)
	file := filepath.Join(t.TempDir(), "wellknown.json")
	if err := runApp(m, "wellknown", "--write", file); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	handler := m.doorHandler(matrix, file, log.New(&logs, "", 0))
	for _, test := range []struct{ path, target, wantPath string }{
		{"/_bridges/instagram/_matrix/provision/v3/login", "127.0.0.1:29320", "/_matrix/provision/v3/login"},
		{"/_bridges/whatsapp/_matrix/provision/v3/login", "127.0.0.1:29318", "/_matrix/provision/v3/login"},
		{"/_bridges/instagram", "127.0.0.1:29320", "/"},
		{"/_bridges/whatsapp/", "127.0.0.1:29318", "/"},
		{"/_bridges/whatsapp/a%2Fb", "127.0.0.1:29318", "/a/b"},
		{"/_matrix/client/v3/sync", matrix.Host, "/_matrix/client/v3/sync"},
		{"/_matrix/federation/v1/version", matrix.Host, "/_matrix/federation/v1/version"},
		{"/.well-known/matrix/client", matrix.Host, "/.well-known/matrix/client"},
		{"/.well-known/matrix/server", matrix.Host, "/.well-known/matrix/server"},
		{"/other/private-id", matrix.Host, "/other/private-id"},
	} {
		t.Run(test.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "https://door.example"+test.path+"?access_token=query-secret&since=a%2Fb", strings.NewReader("body-secret"))
			r.Host = "door.example"
			r.RemoteAddr = "192.0.2.15:1234"
			r.TLS = &tls.ConnectionState{}
			r.Header.Set("Authorization", "Bearer auth-secret")
			r.Header.Set("Cookie", "cookie-secret")
			r.Header.Set("X-Forwarded-For", "spoofed")
			r.Header.Set("X-Forwarded-Proto", "http")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			var echo doorEcho
			if err := json.Unmarshal(w.Body.Bytes(), &echo); err != nil {
				t.Fatal(err)
			}
			if w.Code != 201 || echo.Target != test.target || echo.Path != test.wantPath || echo.Host != "door.example" || echo.ForwardedFor != "192.0.2.15" || echo.Proto != "https" || echo.Query != r.URL.RawQuery || echo.Authorization != "Bearer auth-secret" || echo.Cookie != "cookie-secret" || echo.Body != "body-secret" {
				t.Fatalf("bad proxy response: status=%d %+v", w.Code, echo)
			}
			if strings.Contains(test.path, "%2F") && !strings.Contains(echo.RawPath, "%2F") {
				t.Fatal("escaped suffix lost")
			}
		})
	}
	for _, path := range []string{"/_bridges", "/_bridges/", "/_bridges/unknown/provision", "/_bridges/instagram-evil/x", "/_bridges%2Finstagram/x"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "https://door.example"+path, nil))
		if w.Code != 404 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	for _, value := range []string{"query-secret", "auth-secret", "body-secret", "cookie-secret", "private-id", "since=", "spoofed"} {
		if strings.Contains(logs.String(), value) {
			t.Fatalf("sensitive request data logged: %s", value)
		}
	}
	if count := strings.Count(logs.String(), "\n"); count != 15 {
		t.Fatalf("got %d log lines, expected 15", count)
	}
	if err := os.Remove(m.bridgePath(Bridge{Name: "whatsapp"}, ".registered")); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "https://door.example/_bridges/whatsapp/x", nil))
	if w.Code != 404 {
		t.Fatalf("removed inventory still routes: %d", w.Code)
	}
	if err := os.WriteFile(m.bridgePath(Bridge{Name: "instagram"}, "registration.native.yaml"), []byte("id: invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "https://door.example/_bridges/instagram/x", nil))
	if w.Code != 503 {
		t.Fatalf("invalid inventory did not fail closed: %d", w.Code)
	}
}

func TestDoorWellKnownFileChanges(t *testing.T) {
	m, _, _ := fixture(t)
	target, _ := url.Parse("http://127.0.0.1:1")
	file := filepath.Join(t.TempDir(), "mautrix.json")
	handler := m.doorHandler(target, file, log.New(io.Discard, "", 0))
	for _, content := range []string{"", `{"fi.mau.bridges":[]}`, `{"fi.mau.bridges":["updated"]}`, ""} {
		if content != "" {
			if err := os.WriteFile(file, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "https://door.example/.well-known/matrix/mautrix?secret=hidden", nil))
		if content == "" {
			if w.Code != 404 {
				t.Fatal(w.Code)
			}
		} else if w.Code != 200 || w.Body.String() != content || w.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("wellknown mismatch: %d %q", w.Code, w.Body.String())
		}
	}
}

func TestDoorStreamsBeforeUpstreamCompletes(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "12")
		_, _ = io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = io.WriteString(w, "final\n")
	}))
	defer upstream.Close()
	defer close(release)
	m, _, _ := fixture(t)
	target, _ := url.Parse(upstream.URL)
	door := httptest.NewTLSServer(m.doorHandler(target, "unused", log.New(io.Discard, "", 0)))
	defer door.Close()
	client := door.Client()
	client.Timeout = 2 * time.Second
	response, err := client.Get(door.URL + "/_matrix/client/v3/sync")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, 6)
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "first\n" {
		t.Fatalf("first chunk buffered: %q %v", first, err)
	}
}

func validDoorOptions(t *testing.T) doorOptions {
	t.Helper()
	return doorOptions{Listen: []string{"127.0.0.1:0"}, Hostname: "test.tailnet.ts.net", CertDir: filepath.Join(t.TempDir(), "certs"), MatrixUpstream: "http://127.0.0.1:8008", WellKnownFile: "synthetic.json", TailscaleBin: "tailscale", WaitTimeout: 20 * time.Millisecond}
}

func TestDoorRejectsUnsafeOptionsBeforeEffects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*doorOptions)
	}{
		{"IPv4 wildcard", func(o *doorOptions) { o.Listen = []string{"0.0.0.0:8448"} }},
		{"IPv6 wildcard", func(o *doorOptions) { o.Listen = []string{"[::]:8448"} }},
		{"mapped wildcard", func(o *doorOptions) { o.Listen = []string{"[::ffff:0.0.0.0]:8448"} }},
		{"DNS listener", func(o *doorOptions) { o.Listen = []string{"localhost:8448"} }},
		{"duplicate", func(o *doorOptions) { o.Listen = append(o.Listen, o.Listen[0]) }},
		{"no wait", func(o *doorOptions) { o.WaitTimeout = 0 }},
		{"unsafe hostname", func(o *doorOptions) { o.Hostname = "../test.ts.net" }},
		{"remote upstream", func(o *doorOptions) { o.MatrixUpstream = "http://192.0.2.1:8008" }},
		{"credentials", func(o *doorOptions) { o.MatrixUpstream = "http://user:secret@127.0.0.1:8008" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m, _, runner := fixture(t)
			options := validDoorOptions(t)
			test.mutate(&options)
			if err := m.runDoor(context.Background(), options); err == nil {
				t.Fatal("accepted unsafe options")
			}
			if _, err := os.Stat(options.CertDir); !os.IsNotExist(err) || len(runner.calls) != 0 {
				t.Fatal("effects before validation")
			}
		})
	}
}

func TestDoorWaitForEveryIPAndTimeout(t *testing.T) {
	addresses := []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0"), netip.MustParseAddrPort("[::1]:0")}
	addr := func(cidr string) net.Addr {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		return network
	}
	calls := 0
	if err := waitDoorIPs(context.Background(), addresses, 2*time.Second, func() ([]net.Addr, error) {
		calls++
		if calls == 1 {
			return []net.Addr{addr("127.0.0.1/32")}, nil
		}
		return []net.Addr{addr("127.0.0.1/32"), addr("::1/128")}, nil
	}); err != nil || calls != 2 {
		t.Fatalf("did not wait for both addresses: %v, calls=%d", err, calls)
	}
	err := waitDoorIPs(context.Background(), addresses, time.Millisecond, func() ([]net.Addr, error) { return []net.Addr{addr("127.0.0.1/32")}, nil })
	if err == nil || !strings.Contains(err.Error(), "::1") {
		t.Fatalf("missing timeout detail: %v", err)
	}
	m, _, runner := fixture(t)
	options := validDoorOptions(t)
	options.Listen = []string{"192.0.2.123:8448"}
	err = m.runDoor(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "192.0.2.123") {
		t.Fatalf("nonlocal IP accepted: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("certificate issuance before IP appeared")
	}
	if _, err := os.Stat(options.CertDir); !os.IsNotExist(err) {
		t.Fatal("cert directory created before local IP check")
	}
}

func TestDoorProxyFailureDoesNotLogSecrets(t *testing.T) {
	m, _, _ := fixture(t)
	m.HTTP.Transport = roundTripper(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("secret-upstream-error") })
	var logs bytes.Buffer
	target, _ := url.Parse("http://127.0.0.1:1")
	w := httptest.NewRecorder()
	m.doorHandler(target, "unused", log.New(&logs, "", 0)).ServeHTTP(w, httptest.NewRequest("GET", "https://door.example/private?secret=query", nil))
	if w.Code != 502 || strings.Contains(logs.String(), "secret") || strings.Count(logs.String(), "\n") != 1 {
		t.Fatalf("unsafe failure: %d %q", w.Code, logs.String())
	}
}
