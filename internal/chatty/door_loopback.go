package chatty

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
)

func validLoopbackAuthority(request *http.Request) bool {
	local, ok := request.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	if !ok || !local.IP.IsLoopback() {
		return false
	}
	// Never derive a token destination from an arbitrary Host or forwarded header.
	// CHATTY.md documents this allowlist; removing --http-listen disables HTTP.
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		if request.Host == net.JoinHostPort(host, strconv.Itoa(local.Port)) {
			return true
		}
		if local.Port == 80 && (request.Host == "localhost" || request.Host == "127.0.0.1" || request.Host == "[::1]") {
			return true
		}
	}
	return false
}

func (m *Manager) serveLoopbackDiscovery(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	bridges, err := m.registeredBridges()
	if err != nil {
		http.Error(response, "inventory unavailable", http.StatusServiceUnavailable)
		return
	}
	// The public snapshot stays HTTPS; local sessions require their own exact origin.
	// See CHATTY.md. This request-local view never rewrites the published file.
	document := bridgeWellKnown{Bridges: []string{}}
	for _, bridge := range bridges {
		document.Bridges = append(document.Bridges, "http://"+request.Host+"/_bridges/"+bridge.Name)
	}
	response.Header().Set("Content-Type", "application/json")
	if request.Method != http.MethodHead {
		_ = json.NewEncoder(response).Encode(document)
	}
}
