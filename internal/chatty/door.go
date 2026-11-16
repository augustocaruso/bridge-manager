package chatty

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"
)

type doorOptions struct {
	Listen                                                         []string
	HTTPListen                                                     []string
	Hostname, CertDir, MatrixUpstream, WellKnownFile, TailscaleBin string
	WaitTimeout                                                    time.Duration
}

func doorCommand(m *Manager) *cli.Command {
	return &cli.Command{
		Name: "door", Usage: "Serve private HTTPS: Matrix, discovery and provisioning on the same origin",
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "listen", Required: true, Usage: "Local literal IP:port; repeat for IPv4 and [IPv6]:port"},
			&cli.StringSliceFlag{Name: "http-listen", Usage: "Optional HTTP on literal loopback IP:port only; repeat for IPv4/IPv6"},
			&cli.StringFlag{Name: "hostname", Required: true, Usage: "DNS name for the Tailscale certificate"},
			&cli.StringFlag{Name: "cert-dir", Required: true, Usage: "Private certificate directory (0700)"},
			&cli.StringFlag{Name: "matrix-upstream", Value: "http://127.0.0.1:8008", Usage: "Matrix HTTP loopback endpoint"},
			&cli.StringFlag{Name: "wellknown-file", Required: true, Usage: "File published by wellknown --write"},
			&cli.StringFlag{Name: "tailscale-bin", Value: "tailscale", Usage: "Tailscale executable, resolved through PATH or an explicit path"},
			&cli.StringFlag{Name: "backend-config", Usage: "Local installation, readers and v1 health probes JSON (optional)"},
			&cli.DurationFlag{Name: "wait-timeout", Value: 120 * time.Second, Usage: "Total wait for local IPs before binding"},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() != 0 {
				return errors.New("extra arguments are not allowed")
			}
			if path := c.String("backend-config"); path != "" {
				if err := m.requireServerName(); err != nil {
					return err
				}
				config, err := loadBackendConfig(path)
				if err != nil {
					return err
				}
				m.Backend = config
			}
			m.BackendHTTPSOrigin = "https://" + c.String("hostname")
			ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
			defer stop()
			return m.runDoor(ctx, doorOptions{Listen: c.StringSlice("listen"), HTTPListen: c.StringSlice("http-listen"), Hostname: c.String("hostname"), CertDir: c.String("cert-dir"), MatrixUpstream: c.String("matrix-upstream"), WellKnownFile: c.String("wellknown-file"), TailscaleBin: c.String("tailscale-bin"), WaitTimeout: c.Duration("wait-timeout")})
		},
	}
}

func validateDoor(options doorOptions) ([]netip.AddrPort, *url.URL, error) {
	if options.WaitTimeout <= 0 || len(options.Listen) == 0 {
		return nil, nil, errors.New("supply --listen and a positive --wait-timeout")
	}
	var addresses []netip.AddrPort
	seen := map[netip.AddrPort]bool{}
	for _, value := range options.Listen {
		address, err := netip.ParseAddrPort(value)
		if err != nil || address.Addr().IsUnspecified() || address.Addr().IsMulticast() || address.Addr().Zone() != "" || address.Addr().Is4In6() {
			return nil, nil, errors.New("--listen requires a specific literal IP (never 0.0.0.0 or ::), without zone, and a port")
		}
		if seen[address] {
			return nil, nil, errors.New("duplicate --listen")
		}
		seen[address] = true
		addresses = append(addresses, address)
	}
	if !validDoorHostname(options.Hostname) || options.CertDir == "" || options.WellKnownFile == "" || options.TailscaleBin == "" {
		return nil, nil, errors.New("supply a valid .ts.net DNS hostname, cert-dir, wellknown-file and tailscale-bin")
	}
	target, err := url.Parse(options.MatrixUpstream)
	if err != nil || target.Scheme != "http" || target.User != nil || target.RawQuery != "" || target.ForceQuery || target.Fragment != "" || target.Opaque != "" || (target.Path != "" && target.Path != "/") {
		return nil, nil, errors.New("--matrix-upstream requires an HTTP loopback origin without credentials, path or query")
	}
	ip, err := netip.ParseAddr(target.Hostname())
	if err != nil || !ip.IsLoopback() {
		return nil, nil, errors.New("--matrix-upstream must use a literal loopback IP")
	}
	if _, err := net.ResolveTCPAddr("tcp", target.Host); err != nil {
		return nil, nil, errors.New("invalid --matrix-upstream port")
	}
	for _, value := range options.HTTPListen {
		address, err := netip.ParseAddrPort(value)
		if err != nil || !address.Addr().IsLoopback() || address.Addr().Is4In6() || address.Addr().Zone() != "" {
			return nil, nil, errors.New("--http-listen requires a literal loopback IP without zone and a port")
		}
		upstreamAddress, _ := netip.ParseAddrPort(target.Host)
		if seen[address] || (address.Addr() == upstreamAddress.Addr().Unmap() && address.Port() == upstreamAddress.Port()) {
			return nil, nil, errors.New("--http-listen duplicated or conflicts with TLS listener or matrix-upstream")
		}
		seen[address] = true
		addresses = append(addresses, address)
	}
	return addresses, target, nil
}

func validDoorHostname(hostname string) bool {
	return strings.HasSuffix(hostname, ".ts.net") && validDNSHostname(hostname)
}

func validDNSHostname(hostname string) bool {
	if len(hostname) == 0 || len(hostname) > 253 {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
				return false
			}
		}
	}
	return true
}

func waitDoorIPs(ctx context.Context, addresses []netip.AddrPort, timeout time.Duration, interfaceAddrs func() ([]net.Addr, error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for local IPs canceled: %w", err)
		}
		local, err := interfaceAddrs()
		if err != nil {
			return errors.New("cannot query local interface IPs")
		}
		present := map[netip.Addr]bool{}
		for _, address := range local {
			if prefix, err := netip.ParsePrefix(address.String()); err == nil {
				present[prefix.Addr().Unmap()] = true
			}
		}
		var missing []string
		for _, address := range addresses {
			if !present[address.Addr()] {
				missing = append(missing, address.Addr().String())
			}
		}
		if len(missing) == 0 {
			return nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for local IPs ended (%s): %w", strings.Join(missing, ", "), ctx.Err())
		case <-timer.C:
		}
	}
}

func doorProxy(target *url.URL, prefix string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.Out.URL.Scheme = target.Scheme
			request.Out.URL.Host = target.Host
			// Preserve Host and the raw query for Matrix signatures and opaque sync tokens.
			// CHATTY.md documents this transparent origin; there is no rewrite toggle.
			request.Out.Host = request.In.Host
			request.Out.URL.RawQuery = request.In.URL.RawQuery
			if prefix != "" {
				request.Out.URL.Path = strings.TrimPrefix(request.In.URL.Path, prefix)
				request.Out.URL.RawPath = strings.TrimPrefix(request.In.URL.EscapedPath(), prefix)
				if request.Out.URL.Path == "" {
					request.Out.URL.Path = "/"
					request.Out.URL.RawPath = ""
				}
			}
			request.SetXForwarded()
		},
		// Immediate flushing and no server write timeout are deliberate for sync/media.
		// See CHATTY.md; buffering and write deadlines are not configurable here.
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		},
	}
}

func (m *Manager) doorHandler(target *url.URL, wellKnownFile string, logger *log.Logger) http.Handler {
	matrix := doorProxy(target, "")
	matrix.Transport = m.HTTP.Transport
	bridges := map[string]*httputil.ReverseProxy{}
	for _, bridge := range nativeDiscoveryBridges() {
		upstream, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", bridge.Port))
		bridges[bridge.Name] = doorProxy(upstream, "/_bridges/"+bridge.Name)
		bridges[bridge.Name].Transport = m.HTTP.Transport
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := &doorResponse{ResponseWriter: w}
		prefix := "/matrix"
		defer func() { logger.Printf("method=%q prefix=%q status=%d", r.Method, prefix, response.statusCode()) }()
		if r.URL.Path == "/_chatty/backend" || strings.HasPrefix(r.URL.Path, "/_chatty/backend/") {
			prefix = "/_chatty/backend"
			m.serveBackend(response, r, target)
			return
		}
		if r.TLS == nil {
			if !validLoopbackAuthority(r) {
				http.Error(response, "local origin unauthorized", http.StatusForbidden)
				return
			}
			if r.URL.Path == "/.well-known/matrix/mautrix" {
				prefix = "/.well-known/matrix/mautrix"
				m.serveLoopbackDiscovery(response, r)
				return
			}
		}
		if r.URL.Path == "/.well-known/matrix/mautrix" {
			prefix = "/.well-known/matrix/mautrix"
			data, err := os.ReadFile(wellKnownFile)
			if os.IsNotExist(err) {
				http.NotFound(response, r)
				return
			}
			if err != nil {
				http.Error(response, "discovery unavailable", http.StatusServiceUnavailable)
				return
			}
			response.Header().Set("Content-Type", "application/json")
			// The file is explicitly republished by wellknown --write; do not cache old inventory.
			response.Header().Set("Cache-Control", "no-store")
			if r.Method != http.MethodHead {
				_, _ = response.Write(data)
			}
			return
		}
		if r.URL.Path == "/_bridges" || strings.HasPrefix(r.URL.Path, "/_bridges/") {
			prefix = "/_bridges/unknown"
			name := strings.Split(strings.TrimPrefix(r.URL.Path, "/_bridges/"), "/")[0]
			proxy, ok := bridges[name]
			if !ok {
				http.NotFound(response, r)
				return
			}
			// Encoded route separators cannot disagree with the raw prefix stripped upstream.
			routePrefix := "/_bridges/" + name
			if !strings.HasPrefix(r.URL.EscapedPath(), routePrefix+"/") && r.URL.EscapedPath() != routePrefix {
				http.NotFound(response, r)
				return
			}
			inventory, err := m.registeredBridges()
			if err != nil {
				http.Error(response, "inventory unavailable", http.StatusServiceUnavailable)
				return
			}
			for _, bridge := range inventory {
				if bridge.Name == name {
					prefix = routePrefix
					proxy.ServeHTTP(response, r)
					return
				}
			}
			http.NotFound(response, r)
			return
		}
		matrix.ServeHTTP(response, r)
	})
}

type doorResponse struct {
	http.ResponseWriter
	status int
}

func (w *doorResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *doorResponse) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
func (w *doorResponse) WriteHeader(status int) {
	if w.status == 0 && (status >= 200 || status == http.StatusSwitchingProtocols) {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *doorResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (m *Manager) runDoor(ctx context.Context, options doorOptions) error {
	addresses, target, err := validateDoor(options)
	if err != nil {
		return err
	}
	if err := waitDoorIPs(ctx, addresses, options.WaitTimeout, net.InterfaceAddrs); err != nil {
		return err
	}
	if _, err := m.registeredBridges(); err != nil {
		return err
	}
	certificates := &doorCertificates{dir: options.CertDir, hostname: options.Hostname, binary: options.TailscaleBin, runner: m.Runner, now: m.Now}
	if err := certificates.check(ctx); err != nil {
		return err
	}
	var listeners []net.Listener
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for _, address := range addresses {
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address.String())
		if err != nil {
			return fmt.Errorf("cannot listen on %s", address)
		}
		listeners = append(listeners, listener)
	}
	logger := log.New(m.Out, "", log.LstdFlags)
	server := &http.Server{Handler: m.doorHandler(target, options.WellKnownFile, logger), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certificates.getCertificate}, ReadHeaderTimeout: 15 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	defer server.Close()
	// Serve and ServeTLS must not race the same server's one-time HTTP/2 setup.
	// CHATTY.md describes the separate HTTP server; omit --http-listen to disable it.
	httpServer := &http.Server{Handler: server.Handler, ReadHeaderTimeout: server.ReadHeaderTimeout, ErrorLog: server.ErrorLog}
	defer httpServer.Close()
	results := make(chan error, len(listeners))
	for index, listener := range listeners {
		go func() {
			if index < len(options.Listen) {
				results <- server.ServeTLS(listener, "", "")
			} else {
				results <- httpServer.Serve(listener)
			}
		}()
	}
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			httpShutdown := make(chan error, 1)
			go func() { httpShutdown <- httpServer.Shutdown(shutdown) }()
			return errors.Join(server.Shutdown(shutdown), <-httpShutdown)
		case err := <-results:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return errors.New("door listener terminated unexpectedly")
		case <-ticker.C:
			if err := certificates.check(ctx); err != nil {
				logger.Print("certificate renewal failed; previous certificate preserved")
			}
		}
	}
}
