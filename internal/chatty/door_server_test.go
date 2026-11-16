package chatty

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reserveDoorAddress(t *testing.T, address string) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func TestDoorRunTLSOnBothLoopbackFamilies(t *testing.T) {
	m, _, _ := fixture(t)
	m.Out = io.Discard
	m.Now = time.Now
	m.Runner = ExecRunner{}
	now := time.Now()
	cert, key := syntheticDoorCertificate(t, 1, "test.tailnet.ts.net", now.Add(-time.Hour), now.Add(90*24*time.Hour))
	options := validDoorOptions(t)
	options.TailscaleBin = fakeDoorBinary(t, cert, key)
	ipv4 := reserveDoorAddress(t, "127.0.0.1:0")
	ipv6 := reserveDoorAddress(t, "[::1]:0")
	options.Listen = []string{ipv4.Addr().String(), ipv6.Addr().String()}
	_ = ipv4.Close()
	_ = ipv6.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Test-Host", r.Host)
		w.Header().Set("Test-Proto", r.Header.Get("X-Forwarded-Proto"))
		_, _ = fmt.Fprintf(w, "matrix bytes=%d", count)
	}))
	defer upstream.Close()
	options.MatrixUpstream = upstream.URL
	options.WellKnownFile = filepath.Join(t.TempDir(), "mautrix.json")
	if err := os.WriteFile(options.WellKnownFile, []byte(`{"fi.mau.bridges":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
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
			t.Error("door failed to shut down")
		}
	}()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		t.Fatal("invalid synthetic root")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: options.Hostname}, ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	for _, address := range options.Listen {
		deadline := time.Now().Add(3 * time.Second)
		var response *http.Response
		var err error
		for time.Now().Before(deadline) {
			response, err = client.Get("https://" + address + "/.well-known/matrix/mautrix")
			if err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("TLS listener %s: %v", address, err)
		}
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != 200 || string(data) != `{"fi.mau.bridges":[]}` {
			t.Fatalf("bad discovery over TLS: status=%d err=%v", response.StatusCode, err)
		}
		if response.ProtoMajor != 2 {
			t.Fatalf("HTTP/2 not available on %s", address)
		}
		// Upload beyond typical small JSON caps through the actual TLS server.
		payload := strings.Repeat("x", 3*1024*1024)
		response, err = client.Post("https://"+address+"/_matrix/media/v3/upload", "application/octet-stream", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		data, err = io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || string(data) != fmt.Sprintf("matrix bytes=%d", len(payload)) || response.Header.Get("Test-Host") != address || response.Header.Get("Test-Proto") != "https" {
			t.Fatalf("bad upload through TLS: %v", err)
		}
	}
	conn, err := tls.Dial("tcp", options.Listen[0], &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11})
	if err == nil {
		_ = conn.Close()
		t.Fatal("TLS <1.2 accepted")
	}
}

func TestDoorBindFailureClosesEarlierListeners(t *testing.T) {
	for _, mode := range []string{"TLS", "HTTP"} {
		t.Run(mode, func(t *testing.T) {
			m, _, _ := fixture(t)
			m.Out = io.Discard
			m.Now = time.Now
			m.Runner = ExecRunner{}
			now := time.Now()
			cert, key := syntheticDoorCertificate(t, 1, "test.tailnet.ts.net", now.Add(-time.Hour), now.Add(90*24*time.Hour))
			options := validDoorOptions(t)
			options.TailscaleBin = fakeDoorBinary(t, cert, key)
			first := reserveDoorAddress(t, "127.0.0.1:0")
			busy := reserveDoorAddress(t, "127.0.0.1:0")
			firstAddress := first.Addr().String()
			_ = first.Close()
			options.Listen = []string{firstAddress}
			if mode == "TLS" {
				options.Listen = append(options.Listen, busy.Addr().String())
			} else {
				options.HTTPListen = []string{busy.Addr().String()}
			}
			if err := m.runDoor(context.Background(), options); err == nil || !strings.Contains(err.Error(), busy.Addr().String()) {
				t.Fatalf("bind failure hidden: %v", err)
			}
			recovered, err := net.Listen("tcp", firstAddress)
			if err != nil {
				t.Fatalf("first listener leaked after second bind failed: %v", err)
			}
			_ = recovered.Close()
		})
	}
}
