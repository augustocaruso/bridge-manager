package chatty

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

type backendHTTPError struct {
	SchemaVersion int `json:"schemaVersion"`
	Error         struct {
		Code      ReasonCode `json:"code"`
		Retryable bool       `json:"retryable"`
	} `json:"error"`
}

func writeBackendError(response http.ResponseWriter, request *http.Request, status int, code ReasonCode, retryable bool) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	if request.Method != http.MethodHead {
		document := backendHTTPError{SchemaVersion: 1}
		document.Error.Code = code
		document.Error.Retryable = retryable
		_ = json.NewEncoder(response).Encode(document)
	}
}
func (m *Manager) serveBackend(response http.ResponseWriter, request *http.Request, target *url.URL) {
	response.Header().Set("Cache-Control", "no-store")
	if request.URL.Path != backendHealthPath || request.URL.EscapedPath() != backendHealthPath {
		writeBackendError(response, request, 404, "invalidRequest", false)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		writeBackendError(response, request, 405, "invalidRequest", false)
		return
	}
	transport, origin, valid := backendOrigin(request, m.BackendHTTPSOrigin)
	if !valid {
		writeBackendError(response, request, 403, "unauthorized", false)
		return
	}
	if request.URL.RawQuery != "" || request.URL.ForceQuery {
		writeBackendError(response, request, 400, "invalidRequest", false)
		return
	}
	headers := request.Header.Values("Authorization")
	if len(headers) == 0 {
		writeBackendError(response, request, 401, "credentialRequired", false)
		return
	}
	if len(headers) != 1 || !strings.HasPrefix(headers[0], "Bearer ") || len(headers[0]) <= 7 || strings.ContainsAny(headers[0][7:], " \t\r\n") {
		writeBackendError(response, request, 401, "unauthorized", false)
		return
	}
	if m.Backend == nil {
		writeBackendError(response, request, 503, "notChecked", false)
		return
	}
	var identity struct {
		UserID string `json:"user_id"`
	}
	reason := m.backendGET(request.Context(), strings.TrimRight(target.String(), "/"), "/_matrix/client/v3/account/whoami", headers[0][7:], &identity)
	if reason.Code != "none" {
		if reason.Code == "credentialRequired" || reason.Code == "unauthorized" {
			writeBackendError(response, request, 401, "unauthorized", false)
		} else {
			writeBackendError(response, request, 503, reason.Code, reason.Retryable)
		}
		return
	}
	if !strings.HasPrefix(identity.UserID, "@") || !strings.Contains(identity.UserID, ":") {
		writeBackendError(response, request, 503, "invalidResponse", true)
		return
	}
	allowed := false
	for _, reader := range m.Backend.Readers {
		if reader == identity.UserID {
			allowed = true
		}
	}
	if !allowed {
		writeBackendError(response, request, 403, "unauthorized", false)
		return
	}
	snapshot, err := m.CollectBackendHealth(request.Context(), *m.Backend, transport, origin)
	if err != nil {
		writeBackendError(response, request, 503, "invalidResponse", true)
		return
	}
	// The responding door is direct service evidence, not a loopback self-request
	// (which would recurse). CLI observations still use the existing supervisor.
	for index := range snapshot.Components {
		component := &snapshot.Components[index]
		if component.ID == "chattyBridges" {
			applyServiceReason(component, HealthReason{Code: "none"}, "service", m.Now().UTC())
			component.Authorization = "granted"
		}
	}
	snapshot.Expire(m.Now().UTC())
	data, err := encodeBackendHealth(snapshot)
	if err != nil {
		writeBackendError(response, request, 503, "invalidResponse", true)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	if request.Method != http.MethodHead {
		_, _ = response.Write(data)
	}
}
