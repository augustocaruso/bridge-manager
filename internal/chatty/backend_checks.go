package chatty

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v2"
)

// BackendConfig is local operator input, never decoded from an HTTP request.
// An installation ID must be provisioned explicitly, not created by anonymous GET.
// See CHATTY.md; omitting --backend-config keeps health unconfigured.
type BackendConfig struct {
	InstallationID  string         `json:"installationID"`
	ValiditySeconds int            `json:"validitySeconds"`
	Readers         []string       `json:"readers"`
	Components      []BackendProbe `json:"components"`
}
type BackendProbe struct {
	ID      ComponentID `json:"id"`
	Binary  string      `json:"binary"`
	Cluster string      `json:"cluster"`
	Origin  string      `json:"origin"`
	Token   string      `json:"token"`
	Host    string      `json:"host"`
	Port    int         `json:"port"`
}

func (config BackendConfig) validate() error {
	if !backendUUID.MatchString(config.InstallationID) || config.ValiditySeconds <= 0 || int64(config.ValiditySeconds) > int64((1<<63-1)/time.Second) || len(config.Components) == 0 || len(config.Components) > 64 {
		return errors.New("invalid health configuration")
	}
	seen := map[ComponentID]bool{}
	for _, probe := range config.Components {
		if !knownComponent(probe.ID) || seen[probe.ID] {
			return errors.New("invalid or duplicate component")
		}
		seen[probe.ID] = true
		if probe.Binary != "" && (!filepath.IsAbs(probe.Binary) || filepath.Clean(probe.Binary) != probe.Binary) || probe.Cluster != "" && (!filepath.IsAbs(probe.Cluster) || filepath.Clean(probe.Cluster) != probe.Cluster) {
			return errors.New("inventory requires absolute local paths")
		}
		if probe.Origin != "" && validateHomeserver(probe.Origin) != nil {
			return errors.New("probe requires a literal HTTP loopback origin")
		}
		if strings.ContainsAny(probe.Token, "\r\n") {
			return errors.New("invalid probe credential")
		}
		if strings.HasPrefix(string(probe.ID), "postgres.") && (probe.Binary == "" || probe.Cluster == "" || net.ParseIP(probe.Host) == nil || !net.ParseIP(probe.Host).IsLoopback() || probe.Port < 1 || probe.Port > 65535) {
			return errors.New("PostgreSQL cluster requires binary, directory and loopback host/port")
		}
	}
	for _, probe := range config.Components {
		if probe.ID == "model.embeddinggemma2" {
			if probe.Binary != "" || probe.Cluster != "" {
				return errors.New("model is queried only through the hub catalog")
			}
			for _, hub := range config.Components {
				if hub.ID == "localai" && hub.Origin != probe.Origin {
					return errors.New("model catalog must use the same LocalAI hub")
				}
			}
		}
		for _, dependency := range backendDependencies(probe.ID) {
			if !seen[dependency] {
				return errors.New("dependency not configured")
			}
		}
	}
	readerSeen := map[string]bool{}
	for _, reader := range config.Readers {
		if !strings.HasPrefix(reader, "@") || !strings.Contains(reader, ":") || len(reader) > 1024 || strings.ContainsAny(reader, " \r\n\t") || readerSeen[reader] {
			return errors.New("invalid or duplicate reader")
		}
		readerSeen[reader] = true
	}
	return nil
}
func loadBackendConfig(path string) (*BackendConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("health configuration unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, backendMaxBytes+1))
	if err != nil || len(data) > backendMaxBytes {
		return nil, errors.New("invalid health configuration")
	}
	var config BackendConfig
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || config.validate() != nil {
		return nil, errors.New("invalid health configuration")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid health configuration")
	}
	return &config, nil
}
func backendDependencies(id ComponentID) []ComponentID {
	switch id {
	case "bridge.whatsapp":
		return []ComponentID{"tuwunel", "postgres.whatsapp"}
	case "bridge.instagram":
		return []ComponentID{"tuwunel", "postgres.instagram"}
	case "model.embeddinggemma2":
		return []ComponentID{"localai"}
	default:
		return []ComponentID{}
	}
}
func backendComponent(id ComponentID) ComponentHealth {
	ownership := Ownership("observed")
	if id == "surrealdb" || id == "localai" || id == "model.embeddinggemma2" {
		ownership = "shared"
	}
	return ComponentHealth{ID: id, Ownership: ownership, Installation: "unknown", Runtime: "unknown", Authorization: "unknown", Reachability: "unknown", Functionality: "notTested", CheckKind: "notRun", Reason: HealthReason{Code: "notChecked"}, DependsOn: backendDependencies(id)}
}
func probeFailure(err error) ReasonCode {
	if errors.Is(err, os.ErrPermission) {
		return "permissionDenied"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dnsUnavailable"
	}
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return "tlsInvalid"
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	return "unreachable"
}
func (m *Manager) backendGET(ctx context.Context, origin, path, token string, result any) HealthReason {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+path, nil)
	if err != nil {
		return HealthReason{Code: "invalidRequest"}
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := *m.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return HealthReason{Code: probeFailure(err), Retryable: true}
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 200:
		if result != nil {
			data, err := io.ReadAll(io.LimitReader(response.Body, backendMaxBytes+1))
			if err != nil || len(data) > backendMaxBytes || json.Unmarshal(data, result) != nil {
				return HealthReason{Code: "invalidResponse", Retryable: true}
			}
		}
		return HealthReason{Code: "none"}
	case 401:
		return HealthReason{Code: "credentialRequired"}
	case 403:
		return HealthReason{Code: "unauthorized"}
	default:
		return HealthReason{Code: "invalidResponse", Retryable: true}
	}
}
func applyServiceReason(component *ComponentHealth, reason HealthReason, kind CheckKind, now time.Time) {
	component.CheckKind = kind
	component.CheckedAt = &now
	component.Reason = reason
	switch reason.Code {
	case "permissionDenied":
		component.Authorization = "denied"
	case "none":
		component.Installation = "present"
		component.Runtime = "running"
		component.Reachability = "reachable"
		component.Authorization = "notApplicable"
	case "credentialRequired", "unauthorized":
		component.Reachability = "reachable"
		component.Authorization = "required"
		if reason.Code == "unauthorized" {
			component.Authorization = "denied"
		}
	case "unreachable", "dnsUnavailable", "tlsInvalid", "timeout":
		component.Reachability = "unreachable"
	case "invalidResponse":
		component.Reachability = "reachable"
	}
}
func (m *Manager) backendInventory(component *ComponentHealth, path string, directory bool, now time.Time) {
	if path == "" {
		return
	}
	stat := m.Lstat
	if stat == nil {
		stat = os.Lstat
	}
	info, err := backendInventoryPath(path, stat)
	component.CheckKind = "inventory"
	component.CheckedAt = &now
	switch {
	case errors.Is(err, os.ErrPermission):
		component.Reason = HealthReason{Code: "permissionDenied"}
		component.Authorization = "denied"
	case errors.Is(err, os.ErrNotExist):
		component.Installation = "absent"
		component.Reason = HealthReason{Code: "notInstalled"}
	case errors.Is(err, ReasonCode("invalidResponse")):
		component.Reason = HealthReason{Code: "invalidResponse"}
	case err != nil:
		component.Reason = HealthReason{Code: "unknownState"}
	case directory && info.IsDir() || !directory && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0:
		component.Installation = "present"
		component.Authorization = "granted"
		component.Reason = HealthReason{Code: "none"}
	case !directory && info.Mode().IsRegular():
		component.Installation = "present"
		component.Authorization = "denied"
		component.Reason = HealthReason{Code: "permissionDenied"}
	default:
		component.Reason = HealthReason{Code: "invalidResponse"}
	}
}
func versionNumber(value string) *string {
	value = strings.TrimSpace(value)
	if len(value) <= 128 && backendVersion.MatchString(value) {
		return &value
	}
	return nil
}
func (m *Manager) backendInstalledVersion(ctx context.Context, probe BackendProbe) *string {
	if probe.Binary == "" {
		return nil
	}
	flag := "--version"
	if strings.HasPrefix(string(probe.ID), "bridge.") {
		flag = "--version-json"
	}
	output, err := m.Runner.Run(ctx, probe.Binary, flag)
	if err != nil {
		return nil
	}
	if flag == "--version-json" {
		var version struct {
			Tag string `json:"tag"`
		}
		if json.Unmarshal(output, &version) == nil {
			return versionNumber(strings.TrimPrefix(version.Tag, "v"))
		}
		return nil
	}
	return installedVersionOutput(probe.ID, output)
}
func (m *Manager) collectBackendComponent(ctx context.Context, probe BackendProbe) ComponentHealth {
	now := m.Now().UTC()
	component := backendComponent(probe.ID)
	m.backendInventory(&component, probe.Binary, false, now)
	if component.Installation == "present" && component.Reason.Code == "none" {
		component.Version.Installed = m.backendInstalledVersion(ctx, probe)
	}
	if !strings.HasPrefix(string(probe.ID), "postgres.") {
		m.backendRuntime(ctx, &component, probe)
	}
	if strings.HasPrefix(string(probe.ID), "postgres.") {
		binaryPresent := component.Installation == "present" && component.Reason.Code == "none"
		if !binaryPresent {
			return component
		}
		m.backendInventory(&component, probe.Cluster, true, now)
		if component.Installation != "present" || component.Reason.Code != "none" {
			return component
		}
		m.backendRuntime(ctx, &component, probe)
		_, err := m.Runner.Run(ctx, probe.Binary, "-h", probe.Host, "-p", strconv.Itoa(probe.Port), "-t", "3")
		reason := HealthReason{Code: "none"}
		if err != nil {
			var exit *ProcessError
			switch {
			case errors.Is(err, os.ErrPermission):
				reason.Code = "permissionDenied"
			case errors.As(err, &exit) && exit.Code == 1:
				reason.Code = "starting"
			case errors.As(err, &exit) && exit.Code == 2:
				reason.Code = "unreachable"
			default:
				reason.Code = "unknownState"
			}
			reason.Retryable = true
		}
		applyServiceReason(&component, reason, "readiness", m.Now().UTC())
		if reason.Code == "starting" {
			component.Runtime = "starting"
			// pg_isready exit 1 proves a response, not readiness (CONTRACT-V1
			// section 2). Preserve reachability without claiming functionality.
			component.Reachability = "reachable"
		}
		return component
	}
	if probe.ID == "model.embeddinggemma2" {
		if probe.Origin == "" {
			return component
		}
		var catalog struct {
			Data *[]struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		reason := m.backendGET(ctx, probe.Origin, "/v1/models", probe.Token, &catalog)
		applyServiceReason(&component, reason, "catalog", m.Now().UTC())
		// A catalog proves availability, not runtime/load or inference. Refresh never
		// invokes embeddings (CONTRACT-V1 section 2); no model-load toggle exists.
		component.Runtime = "unknown"
		if reason.Code == "none" {
			component.Authorization = "granted"
			if catalog.Data == nil {
				component.Installation = "unknown"
				component.Reason = HealthReason{Code: "invalidResponse", Retryable: true}
				return component
			}
			found := false
			seen := map[string]bool{}
			for _, model := range *catalog.Data {
				if model.ID == "" || seen[model.ID] {
					component.Installation = "unknown"
					component.Reason = HealthReason{Code: "invalidResponse", Retryable: true}
					return component
				}
				seen[model.ID] = true
				if model.ID == "embeddinggemma-2" {
					found = true
				}
			}
			if !found {
				component.Installation = "absent"
				component.Reason = HealthReason{Code: "modelUnavailable"}
			}
		}
		return component
	}
	if probe.Origin == "" {
		return component
	}
	switch probe.ID {
	case "tuwunel":
		var versions struct {
			Versions []string `json:"versions"`
		}
		reason := m.backendGET(ctx, probe.Origin, "/_matrix/client/versions", "", &versions)
		if reason.Code == "none" && len(versions.Versions) == 0 {
			reason = HealthReason{Code: "invalidResponse", Retryable: true}
		}
		applyServiceReason(&component, reason, "service", m.Now().UTC())
		// Federation-disabled 403 is intentionally not a health failure. Installed
		// and running versions remain distinct (CHATTY.md; no fallback changes axes).
		if reason.Code == "none" {
			var server struct {
				Server struct {
					Version string `json:"version"`
				} `json:"server"`
			}
			if m.backendGET(ctx, probe.Origin, "/_matrix/federation/v1/version", "", &server).Code == "none" {
				component.Version.Running = versionNumber(server.Server.Version)
			}
		}
	case "bridge.whatsapp", "bridge.instagram":
		reason := m.backendGET(ctx, probe.Origin, "/_matrix/mau/live", "", nil)
		applyServiceReason(&component, reason, "service", m.Now().UTC())
		if reason.Code == "none" {
			reason = m.backendGET(ctx, probe.Origin, "/_matrix/mau/ready", "", nil)
			applyServiceReason(&component, reason, "readiness", m.Now().UTC())
		}
	case "surrealdb":
		applyServiceReason(&component, m.backendGET(ctx, probe.Origin, "/health", "", nil), "service", m.Now().UTC())
	case "localai":
		applyServiceReason(&component, m.backendGET(ctx, probe.Origin, "/readyz", probe.Token, nil), "readiness", m.Now().UTC())
	}
	return component
}
func (m *Manager) CollectBackendHealth(ctx context.Context, config BackendConfig, transport BackendTransport, origin *string) (BackendHealth, error) {
	if err := m.requireServerName(); err != nil {
		return BackendHealth{}, err
	}
	if err := config.validate(); err != nil {
		return BackendHealth{}, err
	}
	now := m.Now().UTC()
	snapshot := BackendHealth{SchemaVersion: 1, InstallationID: config.InstallationID, ObservationID: newBackendUUID(), MatrixServerName: m.Config.ServerName, Transport: transport, SourceOrigin: origin, ObservedAt: now, ValidUntil: now.Add(time.Duration(config.ValiditySeconds) * time.Second), Components: []ComponentHealth{}, Capabilities: HealthCapabilities{ReadHealth: true}}
	byID := map[ComponentID]int{}
	for _, probe := range config.Components {
		byID[probe.ID] = len(snapshot.Components)
		snapshot.Components = append(snapshot.Components, m.collectBackendComponent(ctx, probe))
	}
	for index := range snapshot.Components {
		component := &snapshot.Components[index]
		for _, dependency := range component.DependsOn {
			state := snapshot.Components[byID[dependency]]
			if (state.Reason.Code != "none" || state.Reachability != "reachable") && (component.Reason.Code == "none" || component.Reason.Code == "notChecked") {
				component.Reason = HealthReason{Code: "dependencyUnavailable", Retryable: true, DependencyID: &dependency}
				break
			}
		}
	}
	snapshot.Expire(m.Now().UTC())
	return snapshot, snapshot.Validate()
}
func newBackendUUID() string {
	value := [16]byte{}
	rand.Read(value[:])
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:])
}
func backendHealthCommand(m *Manager) *cli.Command {
	return &cli.Command{Name: "health", Usage: "Query v1 health without installing, repairing or loading models", Flags: []cli.Flag{&cli.StringFlag{Name: "backend-config", Required: true}, &cli.BoolFlag{Name: "json"}}, Action: func(ctx *cli.Context) error {
		if ctx.NArg() != 0 {
			return errors.New("extra arguments are not allowed")
		}
		if err := m.requireServerName(); err != nil {
			return err
		}
		config, err := loadBackendConfig(ctx.String("backend-config"))
		if err != nil {
			return err
		}
		snapshot, err := m.CollectBackendHealth(ctx.Context, *config, "localXPC", nil)
		if err != nil {
			return err
		}
		if ctx.Bool("json") {
			data, err := encodeBackendHealth(snapshot)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(m.Out, string(data))
			return err
		}
		for _, component := range snapshot.Components {
			fmt.Fprintf(m.Out, "%s: installation=%s runtime=%s reachability=%s reason=%s\n", component.ID, component.Installation, component.Runtime, component.Reachability, component.Reason.Code)
		}
		return nil
	}}
}

func backendOrigin(request *http.Request, canonical string) (BackendTransport, *string, bool) {
	if request.TLS == nil {
		if !validLoopbackAuthority(request) {
			return "", nil, false
		}
		origin := "http://" + request.Host
		return "sessionHTTP", &origin, true
	}
	parsed, err := url.Parse(canonical)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", nil, false
	}
	authority, err := url.Parse("https://" + request.Host)
	if err != nil || authority.User != nil || authority.Path != "" || authority.RawQuery != "" || authority.Fragment != "" {
		return "", nil, false
	}
	port := authority.Port()
	if port == "" {
		port = "443"
	}
	canonicalPort := parsed.Port()
	if canonicalPort == "" {
		canonicalPort = "443"
	}
	if !strings.EqualFold(authority.Hostname(), parsed.Hostname()) || port != canonicalPort {
		return "", nil, false
	}
	return "tailnetHTTPS", &canonical, true
}
