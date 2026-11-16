package chatty

import (
	"bytes"
	"encoding/json"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const backendMaxBytes = 256 * 1024
const backendHealthPath = "/_chatty/backend/v1/health"

type ComponentID string
type Ownership string
type Installation string
type Runtime string
type Authorization string
type Reachability string
type Functionality string
type CheckKind string
type ReasonCode string

func (code ReasonCode) Error() string { return string(code) }

type BackendTransport string

type HealthReason struct {
	Code         ReasonCode   `json:"code"`
	Retryable    bool         `json:"retryable"`
	DependencyID *ComponentID `json:"dependencyID"`
}
type HealthVersion struct {
	Installed *string `json:"installed"`
	Running   *string `json:"running"`
}
type ComponentHealth struct {
	ID                   ComponentID   `json:"id"`
	Ownership            Ownership     `json:"ownership"`
	Installation         Installation  `json:"installation"`
	Runtime              Runtime       `json:"runtime"`
	Authorization        Authorization `json:"authorization"`
	Reachability         Reachability  `json:"reachability"`
	Functionality        Functionality `json:"functionality"`
	CheckKind            CheckKind     `json:"checkKind"`
	CheckedAt            *time.Time    `json:"checkedAt"`
	FunctionalCheckedAt  *time.Time    `json:"functionalCheckedAt"`
	FunctionalValidUntil *time.Time    `json:"functionalValidUntil"`
	Reason               HealthReason  `json:"reason"`
	DependsOn            []ComponentID `json:"dependsOn"`
	Version              HealthVersion `json:"version"`
}
type HealthCapabilities struct {
	ReadHealth bool `json:"readHealth"`
	Mutations  bool `json:"mutations"`
}
type BackendHealth struct {
	SchemaVersion    int                `json:"schemaVersion"`
	InstallationID   string             `json:"installationID"`
	ObservationID    string             `json:"observationID"`
	MatrixServerName string             `json:"matrixServerName"`
	Transport        BackendTransport   `json:"transport"`
	SourceOrigin     *string            `json:"sourceOrigin"`
	ObservedAt       time.Time          `json:"observedAt"`
	ValidUntil       time.Time          `json:"validUntil"`
	Components       []ComponentHealth  `json:"components"`
	Capabilities     HealthCapabilities `json:"capabilities"`
}

var backendUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var backendVersion = regexp.MustCompile(`^[0-9][A-Za-z0-9._+\-]*$`)
var backendIDs = []ComponentID{"tuwunel", "bridge.whatsapp", "bridge.instagram", "postgres.whatsapp", "postgres.instagram", "surrealdb", "localai", "model.embeddinggemma2", "tailscale", "chattyBridges"}

func knownComponent(id ComponentID) bool {
	for _, known := range backendIDs {
		if id == known {
			return true
		}
	}
	return false
}
func oneOf(value string, values string) bool {
	for _, allowed := range strings.Fields(values) {
		if value == allowed {
			return true
		}
	}
	return false
}

// Required fields are checked before decoding: Go zero values must not turn a
// truncated v1 document into a healthy snapshot (CONTRACT-V1 section 1).
func requiredJSON(data json.RawMessage, shape reflect.Type) error {
	if shape.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			return nil
		}
		return requiredJSON(data, shape.Elem())
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return ReasonCode("invalidResponse")
	}
	if shape == reflect.TypeFor[time.Time]() {
		return nil
	}
	switch shape.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil {
			return ReasonCode("invalidResponse")
		}
		for index := 0; index < shape.NumField(); index++ {
			field := shape.Field(index)
			name := field.Tag.Get("json")
			value, ok := object[name]
			if !ok {
				return ReasonCode("invalidResponse")
			}
			if err := requiredJSON(value, field.Type); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var items []json.RawMessage
		if json.Unmarshal(data, &items) != nil {
			return ReasonCode("invalidResponse")
		}
		for _, item := range items {
			if err := requiredJSON(item, shape.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
func boundedJSON(value any) bool {
	switch item := value.(type) {
	case string:
		return utf8.ValidString(item) && len(item) <= 1024
	case []any:
		for _, child := range item {
			if !boundedJSON(child) {
				return false
			}
		}
	case map[string]any:
		for key, child := range item {
			if len(key) > 1024 || !boundedJSON(child) {
				return false
			}
		}
	}
	return true
}
func DecodeBackendHealth(data []byte, now time.Time) (BackendHealth, error) {
	var result BackendHealth
	if len(data) > backendMaxBytes || !utf8.Valid(data) {
		return result, ReasonCode("invalidResponse")
	}
	var raw any
	if json.Unmarshal(data, &raw) != nil || !boundedJSON(raw) {
		return result, ReasonCode("invalidResponse")
	}
	if err := requiredJSON(data, reflect.TypeFor[BackendHealth]()); err != nil {
		return result, err
	}
	if json.Unmarshal(data, &result) != nil {
		return result, ReasonCode("invalidResponse")
	}
	if err := result.Validate(); err != nil {
		return BackendHealth{}, err
	}
	result.Expire(now)
	return result, nil
}
func utc(value time.Time) bool { _, offset := value.Zone(); return !value.IsZero() && offset == 0 }
func (snapshot BackendHealth) Validate() error {
	if snapshot.SchemaVersion != 1 {
		return ReasonCode("unsupportedContract")
	}
	if !backendUUID.MatchString(snapshot.InstallationID) || !backendUUID.MatchString(snapshot.ObservationID) || snapshot.MatrixServerName == "" || len(snapshot.MatrixServerName) > 1024 || strings.ContainsAny(snapshot.MatrixServerName, "/\\@ \t\r\n") {
		return ReasonCode("invalidResponse")
	}
	if !utc(snapshot.ObservedAt) || !utc(snapshot.ValidUntil) || !snapshot.ValidUntil.After(snapshot.ObservedAt) || !snapshot.Capabilities.ReadHealth || snapshot.Capabilities.Mutations || snapshot.Components == nil || len(snapshot.Components) > 64 {
		return ReasonCode("invalidResponse")
	}
	if snapshot.Transport == "localXPC" {
		if snapshot.SourceOrigin != nil {
			return ReasonCode("invalidResponse")
		}
	} else {
		if !oneOf(string(snapshot.Transport), "sessionHTTP tailnetHTTPS") || snapshot.SourceOrigin == nil {
			return ReasonCode("invalidResponse")
		}
		parsed, err := url.Parse(*snapshot.SourceOrigin)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
			return ReasonCode("invalidResponse")
		}
		if snapshot.Transport == "sessionHTTP" {
			if validateHomeserver(*snapshot.SourceOrigin) != nil && !(parsed.Scheme == "http" && parsed.Hostname() == "localhost") {
				return ReasonCode("invalidResponse")
			}
		} else if parsed.Scheme != "https" {
			return ReasonCode("invalidResponse")
		}
	}
	byID := map[ComponentID]ComponentHealth{}
	for _, component := range snapshot.Components {
		if _, duplicate := byID[component.ID]; duplicate || !knownComponent(component.ID) {
			return ReasonCode("invalidResponse")
		}
		if err := component.validate(); err != nil {
			return err
		}
		byID[component.ID] = component
	}
	visiting, visited := map[ComponentID]bool{}, map[ComponentID]bool{}
	var visit func(ComponentID) bool
	visit = func(id ComponentID) bool {
		component, exists := byID[id]
		if !exists || visiting[id] {
			return false
		}
		if visited[id] {
			return true
		}
		visiting[id] = true
		seen := map[ComponentID]bool{}
		for _, dependency := range component.DependsOn {
			if seen[dependency] || !visit(dependency) {
				return false
			}
			seen[dependency] = true
		}
		if component.Reason.DependencyID != nil && !seen[*component.Reason.DependencyID] {
			return false
		}
		visiting[id] = false
		visited[id] = true
		return true
	}
	for id := range byID {
		if !visit(id) {
			return ReasonCode("invalidResponse")
		}
	}
	return nil
}
func (component ComponentHealth) validate() error {
	for _, pair := range []struct{ value, allowed string }{
		{string(component.Ownership), "observed managed shared unknown"}, {string(component.Installation), "present absent unknown"},
		{string(component.Runtime), "running stopped starting notApplicable unknown"}, {string(component.Authorization), "granted required denied notApplicable unknown"},
		{string(component.Reachability), "reachable unreachable notApplicable unknown"}, {string(component.Functionality), "passed failed notTested notApplicable unknown"},
		{string(component.CheckKind), "inventory service readiness catalog functional notRun"},
	} {
		if !oneOf(pair.value, pair.allowed) {
			return ReasonCode("invalidResponse")
		}
	}
	if component.DependsOn == nil || len(component.DependsOn) > 64 || component.Reason.Code == "" || len(component.Reason.Code) > 1024 {
		return ReasonCode("invalidResponse")
	}
	if component.CheckKind == "notRun" && (component.Runtime != "unknown" && component.Runtime != "notApplicable" || component.Installation != "unknown" || component.Reachability != "unknown" && component.Reachability != "notApplicable" || component.Functionality == "passed") {
		return ReasonCode("invalidResponse")
	}
	if (component.CheckKind == "notRun") != (component.CheckedAt == nil) || component.CheckedAt != nil && !utc(*component.CheckedAt) {
		return ReasonCode("invalidResponse")
	}
	if component.Installation == "absent" && component.Runtime == "running" || component.Reason.Code == "none" && (component.Reason.Retryable || component.Reason.DependencyID != nil) {
		return ReasonCode("invalidResponse")
	}
	if component.Reason.Code == "dependencyUnavailable" && component.Reason.DependencyID == nil {
		return ReasonCode("invalidResponse")
	}
	if (component.FunctionalCheckedAt == nil) != (component.FunctionalValidUntil == nil) {
		return ReasonCode("invalidResponse")
	}
	if component.FunctionalCheckedAt != nil && (!utc(*component.FunctionalCheckedAt) || !utc(*component.FunctionalValidUntil) || !component.FunctionalValidUntil.After(*component.FunctionalCheckedAt)) {
		return ReasonCode("invalidResponse")
	}
	if component.Functionality == "passed" && component.FunctionalCheckedAt == nil || component.Functionality == "notTested" && component.FunctionalCheckedAt != nil {
		return ReasonCode("invalidResponse")
	}
	for _, version := range []*string{component.Version.Installed, component.Version.Running} {
		if version != nil && (len(*version) > 128 || !backendVersion.MatchString(*version)) {
			return ReasonCode("invalidResponse")
		}
	}
	return nil
}
func (snapshot *BackendHealth) Expire(now time.Time) {
	stale := now.Before(snapshot.ObservedAt) || !now.Before(snapshot.ValidUntil)
	for index := range snapshot.Components {
		component := &snapshot.Components[index]
		functionalStale := component.FunctionalCheckedAt != nil && (now.Before(*component.FunctionalCheckedAt) || !now.Before(*component.FunctionalValidUntil))
		if stale || functionalStale || component.CheckedAt != nil && now.Before(*component.CheckedAt) {
			component.Reason = HealthReason{Code: "stale", Retryable: true}
			if component.Functionality == "passed" || component.Functionality == "failed" {
				component.Functionality = "unknown"
			}
		}
	}
}
func encodeBackendHealth(snapshot BackendHealth) ([]byte, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(snapshot)
	if err != nil || len(data) > backendMaxBytes {
		return nil, ReasonCode("invalidResponse")
	}
	return data, nil
}
