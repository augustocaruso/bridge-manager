package chatty

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackendHTTPAuthorizationAndPrivacy(t *testing.T) {
	for _, test := range []struct {
		name, token, user  string
		upstream, expected int
		reason             ReasonCode
	}{
		{"missing", "", "", 200, 401, "credentialRequired"},
		{"invalid", "invalid", "", 401, 401, "unauthorized"},
		{"non-reader", "synthetic-session", "@other:synthetic.example", 200, 403, "unauthorized"},
		{"whoami-404", "synthetic-session", "", 404, 503, "invalidResponse"},
		{"whoami-redirect", "synthetic-session", "", 302, 503, "invalidResponse"},
		{"whoami-malformed", "synthetic-session", "", 200, 503, "invalidResponse"},
		{"reader", "synthetic-session", "@reader:synthetic.example", 200, 200, "none"},
	} {
		for _, method := range []string{"GET", "HEAD"} {
			t.Run(test.name+method, func(t *testing.T) {
				whoami, probes := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/_matrix/client/v3/account/whoami" {
						whoami++
						if r.Header.Get("Authorization") != "Bearer "+test.token || r.Method != "GET" {
							t.Error("wrong auth forwarding")
						}
						w.Header().Set("Location", "http://foreign.invalid")
						w.WriteHeader(test.upstream)
						_ = json.NewEncoder(w).Encode(map[string]string{"user_id": test.user, "device_id": "private-device"})
						return
					}
					probes++
					if r.Header.Get("Authorization") == "Bearer synthetic-session" {
						t.Error("session credential leaked into probe")
					}
					if r.URL.Path == "/v1/models" {
						if r.Header.Get("Authorization") != "Bearer synthetic-hub" {
							t.Error("missing dedicated hub token")
						}
						_, _ = io.WriteString(w, `{"data":[{"id":"embeddinggemma-2"}]}`)
					}
				}))
				defer server.Close()
				m, _, runner := fixture(t)
				config := backendTestConfig(server.URL)
				m.Backend = &config
				target, _ := url.Parse(server.URL)
				logs := &bytes.Buffer{}
				request := loopbackRequest("localhost:8008", backendHealthPath)
				request.Method = method
				request.Header.Set("Cookie", "session=not-authority")
				request.Header.Set("X-Forwarded-Host", "attacker.invalid")
				if test.token != "" {
					request.Header.Set("Authorization", "Bearer "+test.token)
				}
				response := httptest.NewRecorder()
				m.doorHandler(target, "missing", log.New(logs, "", 0)).ServeHTTP(response, request)
				if response.Code != test.expected || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Access-Control-Allow-Origin") != "" {
					t.Fatalf("response: status %d body %s", response.Code, response.Body)
				}
				if method == "HEAD" {
					if response.Body.Len() != 0 {
						t.Fatal("HEAD has a body")
					}
				} else if test.expected != 200 {
					var document backendHTTPError
					if json.Unmarshal(response.Body.Bytes(), &document) != nil || document.Error.Code != test.reason {
						t.Fatal("wrong structured error")
					}
				} else {
					snapshot, err := DecodeBackendHealth(response.Body.Bytes(), m.Now())
					if err != nil || snapshot.SourceOrigin == nil || *snapshot.SourceOrigin != "http://localhost:8008" {
						t.Fatalf("invalid projection: %v", err)
					}
				}
				if test.expected != 200 && (probes != 0 || len(runner.calls) != 0) {
					t.Fatal("unauthorized request reached checks")
				}
				if test.token == "" && whoami != 0 {
					t.Fatal("missing credentials reached whoami")
				}
				combined := response.Body.String() + logs.String()
				for _, private := range []string{"synthetic-session", "synthetic-hub", "private-device", "@reader", "@other", m.Config.Home, "attacker.invalid"} {
					if strings.Contains(combined, private) {
						t.Fatalf("private data leaked: %s", private)
					}
				}
			})
		}
	}
}
func TestBackendHTTPRejectsMutationsAndOrigins(t *testing.T) {
	m, _, runner := fixture(t)
	config := backendTestConfig("")
	m.Backend = &config
	m.BackendHTTPSOrigin = "https://test.tailnet.ts.net"
	calls := 0
	m.HTTP.Transport = roundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("must not reach upstream")
	})
	target, _ := url.Parse("http://127.0.0.1:8009")
	handler := m.doorHandler(target, "missing", log.New(io.Discard, "", 0))
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE"} {
		request := loopbackRequest("localhost:8008", backendHealthPath)
		request.Method = method
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 405 || response.Header().Get("Allow") != "GET, HEAD" {
			t.Fatal("mutation not rejected before auth")
		}
	}
	for _, host := range []string{"evil.ts.net", "test.tailnet.ts.net.evil.invalid", "test.tailnet.ts.net:444", "test.tailnet.ts.net/path", "user@test.tailnet.ts.net"} {
		request := httptest.NewRequest("GET", "https://test.tailnet.ts.net"+backendHealthPath, nil)
		request.Host = host
		request.Header.Set("Authorization", "Bearer synthetic-session")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 403 {
			t.Fatalf("foreign TLS host accepted: %s %d", host, response.Code)
		}
	}
	request := loopbackRequest("localhost:8008", backendHealthPath)
	request.Host = "foreign.invalid:8008"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("foreign HTTP host accepted")
	}
	if calls != 0 || len(runner.calls) != 0 {
		t.Fatal("rejected requests reached adapters")
	}
}
func TestBackendWhoamiUnreachableAndTimeout(t *testing.T) {
	for _, failure := range []error{errors.New("synthetic unreachable"), context.DeadlineExceeded} {
		m, _, _ := fixture(t)
		config := backendTestConfig("")
		m.Backend = &config
		m.HTTP.Transport = roundTripper(func(*http.Request) (*http.Response, error) { return nil, failure })
		target, _ := url.Parse("http://127.0.0.1:8009")
		request := loopbackRequest("localhost:8008", backendHealthPath)
		request.Header.Set("Authorization", "Bearer synthetic-session")
		response := httptest.NewRecorder()
		m.serveBackend(response, request, target)
		if response.Code != 503 {
			t.Fatal("validation outage became missing service or invalid credentials")
		}
	}
}
func TestBackendDoorLiveMixedTransports(t *testing.T) {
	m, _, _ := fixture(t)
	m.Config.ServerName = "matrix.example.org"
	m.Now = time.Now
	m.Out = io.Discard
	m.Runner = ExecRunner{}
	var whoami, catalog atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_matrix/client/v3/account/whoami":
			whoami.Add(1)
			if r.Header.Get("Authorization") != "Bearer synthetic-session" {
				w.WriteHeader(401)
				return
			}
			_, _ = io.WriteString(w, `{"user_id":"@reader:synthetic.example"}`)
		case "/readyz":
			if r.Header.Get("Authorization") != "" {
				t.Error("session token reached LocalAI readiness")
			}
		case "/v1/models":
			catalog.Add(1)
			if r.Header.Get("Authorization") != "Bearer synthetic-hub" {
				t.Error("wrong model credential")
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"embeddinggemma-2"}]}`)
		default:
			t.Errorf("unexpected upstream %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	config := backendTestConfig(upstream.URL)
	config.Components = append(config.Components, BackendProbe{ID: "chattyBridges"})
	m.Backend = &config
	options := validDoorOptions(t)
	options.MatrixUpstream = upstream.URL
	cert, key := syntheticDoorCertificate(t, 1, options.Hostname, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))
	options.TailscaleBin = fakeDoorBinary(t, cert, key)
	options.Listen = nil
	options.HTTPListen = nil
	for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		tlsListener := reserveDoorAddress(t, address)
		options.Listen = append(options.Listen, tlsListener.Addr().String())
		_ = tlsListener.Close()
		httpListener := reserveDoorAddress(t, address)
		options.HTTPListen = append(options.HTTPListen, httpListener.Addr().String())
		_ = httpListener.Close()
	}
	// The public authority is configured, not inferred from Host or forwarding.
	m.BackendHTTPSOrigin = "https://" + options.Hostname
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan error, 1)
	go func() { results <- m.runDoor(ctx, options) }()
	defer func() {
		cancel()
		select {
		case err := <-results:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("door shutdown timed out")
		}
		for _, address := range append(options.Listen, options.HTTPListen...) {
			listener, err := net.Listen("tcp", address)
			if err != nil {
				t.Error("listener leaked")
			} else {
				_ = listener.Close()
			}
		}
	}()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		t.Fatal("bad root")
	}
	tlsClient := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: options.Hostname}, ForceAttemptHTTP2: true}}
	httpClient := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer tlsClient.CloseIdleConnections()
	defer httpClient.CloseIdleConnections()
	for _, test := range []struct {
		scheme, address string
		client          *http.Client
		tls             bool
	}{
		{"http", options.HTTPListen[0], httpClient, false}, {"http", options.HTTPListen[1], httpClient, false},
		{"https", options.Listen[0], tlsClient, true}, {"https", options.Listen[1], tlsClient, true},
	} {
		for _, method := range []string{"GET", "HEAD"} {
			request, err := http.NewRequest(method, test.scheme+"://"+test.address+backendHealthPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			expectedOrigin := test.scheme + "://" + test.address
			if test.tls {
				request.Host = options.Hostname
				expectedOrigin = m.BackendHTTPSOrigin
			}
			request.Header.Set("Authorization", "Bearer synthetic-session")
			request.Header.Set("Forwarded", "host=evil.invalid;proto=http")
			var response *http.Response
			deadline := time.Now().Add(3 * time.Second)
			for {
				response, err = test.client.Do(request)
				if err == nil || !time.Now().Before(deadline) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != 200 || test.tls && response.ProtoMajor != 2 {
				t.Fatalf("transport health failed: %s %s status=%d proto=%s body=%s err=%v", method, test.address, response.StatusCode, response.Proto, data, err)
			}
			if method == "HEAD" {
				if len(data) != 0 {
					t.Fatal("HEAD body")
				}
				continue
			}
			snapshot, err := DecodeBackendHealth(data, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.MatrixServerName != "matrix.example.org" || snapshot.SourceOrigin == nil || *snapshot.SourceOrigin != expectedOrigin || snapshot.Capabilities.Mutations || len(snapshot.Components) != 3 || snapshot.Components[1].Installation != "present" || snapshot.Components[1].Functionality != "notTested" {
				t.Fatalf("wrong live contract: %+v", snapshot)
			}
			if snapshot.Components[2].Runtime != "running" || snapshot.Components[2].Reachability != "reachable" {
				t.Fatal("the running door did not observe its own responding health route")
			}
			t.Logf("readback %s %s: %s, v%d, origin=%s, model=%s/%s", method, test.address, response.Proto, snapshot.SchemaVersion, *snapshot.SourceOrigin, snapshot.Components[1].Installation, snapshot.Components[1].Functionality)
		}
	}
	if whoami.Load() != 8 || catalog.Load() != 8 {
		t.Fatalf("HEAD did not authorize/check same as GET: %d %d", whoami.Load(), catalog.Load())
	}
}
func TestBackendCLIJSONAndConfig(t *testing.T) {
	m, output, runner := fixture(t)
	config := backendTestConfig("")
	path := filepath.Join(t.TempDir(), "backend.json")
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runApp(m, "--server-name", "matrix.example.org", "health", "--backend-config", path, "--json"); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := DecodeBackendHealth(output.Bytes(), m.Now()); err != nil || snapshot.MatrixServerName != "matrix.example.org" {
		t.Fatalf("CLI did not produce contract: %v %s", err, output)
	}
	if len(runner.calls) != 0 {
		t.Fatal("unconfigured probes ran commands")
	}
	for _, args := range [][]string{{"health"}, {"health", "--backend-config", path, "extra"}, {"health", "--backend-config", "/synthetic/missing"}} {
		if err := runApp(m, args...); err == nil {
			t.Fatalf("bad CLI accepted: %v", args)
		}
	}
	output.Reset()
	if err := runApp(m, "health", "--help"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "--json") {
		t.Fatal("JSON not discoverable")
	}
}
func TestBackendSafetyRedRegressions(t *testing.T) {
	snapshot, err := DecodeBackendHealth(backendFixture(t), backendFixtureTime())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Components[0] = backendComponent("localai")
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("notRun baseline must be valid: %v", err)
	}
	snapshot.Components[0].Runtime = "running"
	// No observation may assert a running process without having run a check.
	if snapshot.Validate() == nil {
		t.Error("notRun accepted an asserted running process")
	}
	config := backendTestConfig("http://127.0.0.1:1")
	config.Components[1].Origin = "http://127.0.0.1:2"
	if config.validate() == nil {
		t.Error("model catalog accepted outside its configured LocalAI hub")
	}
}
