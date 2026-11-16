package chatty

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loopbackRequest(host, path string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	return request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8008}))
}

func TestDoorLoopbackDiscoverySameOrigin(t *testing.T) {
	manager, _, _ := fixture(t)
	for _, name := range []string{"instagram", "whatsapp"} {
		installDiscoveryFixture(t, manager, name)
	}
	file := filepath.Join(t.TempDir(), "public.json")
	if err := runApp(manager, "wellknown", "--write", file); err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse("http://127.0.0.1:8009")
	handler := manager.doorHandler(target, file, log.New(io.Discard, "", 0))
	for _, host := range []string{"localhost:8008", "127.0.0.1:8008", "[::1]:8008"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			request := loopbackRequest(host, "/.well-known/matrix/mautrix")
			request.Method = method
			request.Header.Set("X-Forwarded-Host", "attacker.example")
			request.Header.Set("X-Forwarded-Proto", "https")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("local discovery headers/status: %d", response.Code)
			}
			if method == http.MethodHead {
				if response.Body.Len() != 0 {
					t.Fatal("HEAD returned a body")
				}
				continue
			}
			var document bridgeWellKnown
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatal(err)
			}
			want := "http://" + host + "/_bridges/instagram,http://" + host + "/_bridges/whatsapp"
			if strings.Join(document.Bridges, ",") != want {
				t.Fatalf("discovery does not match session origin %s", host)
			}
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", discoveryOrigin+"/.well-known/matrix/mautrix", nil))
	if response.Code != 200 || response.Body.String() != string(public) {
		t.Fatal("public HTTPS discovery changed")
	}
	if err := os.Remove(manager.bridgePath(Bridge{Name: "whatsapp"}, ".registered")); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, loopbackRequest("localhost:8008", "/.well-known/matrix/mautrix"))
	if response.Code != 200 || strings.Contains(response.Body.String(), "whatsapp") {
		t.Fatal("local discovery did not refresh inventory")
	}
	if err := os.WriteFile(manager.bridgePath(Bridge{Name: "instagram"}, "registration.native.yaml"), []byte("id: invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, loopbackRequest("localhost:8008", "/.well-known/matrix/mautrix"))
	if response.Code != 503 {
		t.Fatal("invalid local inventory did not fail closed")
	}
}

func TestDoorLoopbackRejectsForeignAuthorityBeforeProxy(t *testing.T) {
	manager, _, _ := fixture(t)
	calls := 0
	manager.HTTP.Transport = roundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("unexpected"))}, nil
	})
	target, _ := url.Parse("http://127.0.0.1:8009")
	handler := manager.doorHandler(target, "missing", log.New(io.Discard, "", 0))
	for _, host := range []string{"attacker.example:8008", "localhost:8009", "localhost", "127.0.0.2:8008", "localhost.:8008", "[::ffff:127.0.0.1]:8008", "LOCALHOST:8008"} {
		for _, path := range []string{"/.well-known/matrix/mautrix", "/_matrix/client/v3/versions", "/_bridges/whatsapp/_matrix/provision/v3/whoami"} {
			request := loopbackRequest("localhost:8008", path)
			request.Host = host
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || calls != 0 {
				t.Fatalf("foreign authority reached handler: %q %s status=%d calls=%d", host, path, response.Code, calls)
			}
		}
	}
}

func TestDoorLoopbackRejectsUnsafeListenersBeforeEffects(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*doorOptions)
	}{
		{"wildcard", func(o *doorOptions) { o.HTTPListen = []string{"0.0.0.0:8008"} }},
		{"IPv6 wildcard", func(o *doorOptions) { o.HTTPListen = []string{"[::]:8008"} }},
		{"LAN", func(o *doorOptions) { o.HTTPListen = []string{"192.0.2.1:8008"} }},
		{"tailnet", func(o *doorOptions) { o.HTTPListen = []string{"100.64.0.10:8008"} }},
		{"DNS", func(o *doorOptions) { o.HTTPListen = []string{"localhost:8008"} }},
		{"mapped", func(o *doorOptions) { o.HTTPListen = []string{"[::ffff:127.0.0.1]:8008"} }},
		{"zone", func(o *doorOptions) { o.HTTPListen = []string{"[::1%lo0]:8008"} }},
		{"duplicate", func(o *doorOptions) { o.HTTPListen = []string{"127.0.0.1:8008", "127.0.0.1:8008"} }},
		{"TLS collision", func(o *doorOptions) { o.HTTPListen = o.Listen }},
		{"upstream collision", func(o *doorOptions) { o.HTTPListen = []string{"127.0.0.1:8008"} }},
		{"IPv6 upstream collision", func(o *doorOptions) {
			o.MatrixUpstream = "http://[0:0:0:0:0:0:0:1]:8008"
			o.HTTPListen = []string{"[::1]:8008"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, _, runner := fixture(t)
			options := validDoorOptions(t)
			test.mutate(&options)
			if err := manager.runDoor(context.Background(), options); err == nil {
				t.Fatal("unsafe HTTP listener accepted")
			}
			if len(runner.calls) != 0 {
				t.Fatal("process executed before validation")
			}
			if _, err := os.Stat(options.CertDir); !os.IsNotExist(err) {
				t.Fatal("certificate directory touched before validation")
			}
		})
	}
}

func TestDoorLoopbackRunHTTPAndProvisioning(t *testing.T) {
	manager, _, _ := fixture(t)
	manager.Out = io.Discard
	manager.Now = time.Now
	manager.Runner = ExecRunner{}
	for _, name := range []string{"instagram", "whatsapp"} {
		installDiscoveryFixture(t, manager, name)
	}
	options := validDoorOptions(t)
	now := time.Now()
	cert, key := syntheticDoorCertificate(t, 1, options.Hostname, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	options.TailscaleBin = fakeDoorBinary(t, cert, key)
	tlsListener := reserveDoorAddress(t, "127.0.0.1:0")
	options.Listen = []string{tlsListener.Addr().String()}
	_ = tlsListener.Close()
	for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		listener := reserveDoorAddress(t, address)
		options.HTTPListen = append(options.HTTPListen, listener.Addr().String())
		_ = listener.Close()
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-loopback-token" || r.Header.Get("X-Forwarded-Proto") != "http" || r.Header.Get("X-Forwarded-For") == "spoofed" {
			t.Error("HTTP proxy lost authentication or trusted spoofed forwarding")
		}
		_ = json.NewEncoder(w).Encode(struct{ Path, Host, Query string }{r.URL.Path, r.Host, r.URL.RawQuery})
	}))
	defer upstream.Close()
	options.MatrixUpstream = upstream.URL
	options.WellKnownFile = filepath.Join(t.TempDir(), "public.json")
	if err := runApp(manager, "wellknown", "--write", options.WellKnownFile); err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(upstream.URL)
	manager.HTTP.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		r.URL.Host = target.Host
		return http.DefaultTransport.RoundTrip(r)
	})
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan error, 1)
	go func() { results <- manager.runDoor(ctx, options) }()
	defer func() {
		cancel()
		select {
		case err := <-results:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("mixed HTTP/TLS server did not shut down")
		}
		for _, address := range append(options.Listen, options.HTTPListen...) {
			listener, err := net.Listen("tcp", address)
			if err != nil {
				t.Errorf("listener not released: %v", err)
			} else {
				_ = listener.Close()
			}
		}
	}()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	for _, address := range options.HTTPListen {
		_, port, _ := net.SplitHostPort(address)
		origin := "http://localhost:" + port
		var response *http.Response
		var err error
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			request, _ := http.NewRequest("GET", "http://"+address+"/.well-known/matrix/mautrix", nil)
			request.Host = "localhost:" + port
			response, err = client.Do(request)
			if err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		var document bridgeWellKnown
		err = json.NewDecoder(response.Body).Decode(&document)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != 200 || len(document.Bridges) != 2 {
			t.Fatalf("HTTP discovery failed: status=%d err=%v", response.StatusCode, err)
		}
		paths := []string{"/_matrix/client/versions"}
		for _, bridge := range document.Bridges {
			if !strings.HasPrefix(bridge, origin+"/_bridges/") {
				t.Fatal("wrong real HTTP origin")
			}
			paths = append(paths, strings.TrimPrefix(bridge, origin)+"/_matrix/provision/v3/whoami", strings.TrimPrefix(bridge, origin)+"/_matrix/provision/v3/login/flows")
		}
		for _, path := range paths {
			request, _ := http.NewRequest("GET", "http://"+address+path+"?user_id=%40test%3Aexample.org", nil)
			request.Host = "localhost:" + port
			request.Header.Set("Authorization", "Bearer synthetic-loopback-token")
			request.Header.Set("X-Forwarded-For", "spoofed")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			var echo struct{ Path, Host, Query string }
			err = json.NewDecoder(response.Body).Decode(&echo)
			_ = response.Body.Close()
			wantPath := path
			if strings.HasPrefix(path, "/_bridges/") {
				wantPath = "/_matrix/" + strings.SplitN(path, "/_matrix/", 2)[1]
			}
			if err != nil || response.StatusCode != 200 || echo.Path != wantPath || echo.Host != request.Host || echo.Query != request.URL.RawQuery {
				t.Fatalf("HTTP proxy contract failed for %s: status=%d err=%v", path, response.StatusCode, err)
			}
		}
		request, _ := http.NewRequest("GET", "http://"+address+"/_matrix/client/versions", nil)
		request.Host = "attacker.example:" + port
		response, err = client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatal("foreign Host accepted over real HTTP")
		}
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		t.Fatal("invalid test certificate")
	}
	tlsClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: options.Hostname}, ForceAttemptHTTP2: true}, Timeout: 2 * time.Second}
	defer tlsClient.CloseIdleConnections()
	response, err := tlsClient.Get("https://" + options.Listen[0] + "/.well-known/matrix/mautrix")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	public, readErr := os.ReadFile(options.WellKnownFile)
	if err != nil || readErr != nil || response.StatusCode != 200 || response.ProtoMajor != 2 || string(data) != string(public) {
		t.Fatal("HTTPS/HTTP2 public discovery changed with HTTP enabled")
	}
}

func TestDoorLoopbackAuthorityRequiresLocalListener(t *testing.T) {
	for _, local := range []net.Addr{nil, &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 8008}} {
		request := httptest.NewRequest("GET", "http://localhost:8008/", nil)
		if local != nil {
			request = request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, local))
		}
		if validLoopbackAuthority(request) {
			t.Fatal("accepted authority without a loopback listener")
		}
	}
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]", "localhost:80", "[::1]:80"} {
		request := httptest.NewRequest("GET", "http://"+host+"/", nil)
		request = request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("::1"), Port: 80}))
		if !validLoopbackAuthority(request) {
			t.Fatalf("rejected default HTTP port for %q", host)
		}
	}
}

func TestDoorLoopbackEmptyInventory(t *testing.T) {
	manager, _, _ := fixture(t)
	target, _ := url.Parse("http://127.0.0.1:8009")
	response := httptest.NewRecorder()
	manager.doorHandler(target, "missing", log.New(io.Discard, "", 0)).ServeHTTP(response, loopbackRequest("localhost:8008", "/.well-known/matrix/mautrix"))
	if response.Code != 200 || strings.TrimSpace(response.Body.String()) != `{"fi.mau.bridges":[]}` {
		t.Fatal("empty inventory was not an empty JSON array")
	}
}

func TestDoorLoopbackCLIFlag(t *testing.T) {
	manager, output, _ := fixture(t)
	if err := runApp(manager, "door", "--help"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "--http-listen") {
		t.Fatal("door has no optional loopback HTTP listener")
	}
}
