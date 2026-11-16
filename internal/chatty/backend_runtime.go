package chatty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (m *Manager) backendRuntime(ctx context.Context, component *ComponentHealth, probe BackendProbe) {
	label := ""
	switch probe.ID {
	case "tuwunel":
		label = m.Config.launchdLabel("tuwunel")
	case "bridge.whatsapp":
		label = m.Config.launchdLabel("whatsapp-bridge")
	case "bridge.instagram":
		label = m.Config.launchdLabel("instagram-bridge")
	case "postgres.whatsapp":
		label = m.Config.launchdLabel("whatsapp-postgres")
	case "postgres.instagram":
		label = m.Config.launchdLabel("instagram-postgres")
	case "chattyBridges":
		label = m.Config.launchdLabel("door")
	}
	if component.Installation != "present" || component.Reason.Code != "none" {
		return
	}
	if probe.ID == "tailscale" && probe.Binary != "" {
		output, err := m.Runner.Run(ctx, probe.Binary, "status", "--json")
		checkedAt := m.Now().UTC()
		component.CheckKind, component.CheckedAt = "service", &checkedAt
		var status struct {
			BackendState string `json:"BackendState"`
		}
		if err != nil {
			component.Reason = HealthReason{Code: "unknownState"}
			if errors.Is(err, os.ErrPermission) {
				component.Reason.Code = "permissionDenied"
				component.Authorization = "denied"
			}
			return
		}
		if json.Unmarshal(output, &status) != nil {
			component.Reason = HealthReason{Code: "invalidResponse"}
			return
		}
		switch status.BackendState {
		case "Running":
			component.Runtime = "running"
			component.Reachability = "reachable"
		case "Stopped":
			component.Runtime = "stopped"
		case "Starting":
			component.Runtime = "starting"
			component.Reason = HealthReason{Code: "starting", Retryable: true}
		case "NeedsLogin":
			component.Authorization = "required"
			component.Reason = HealthReason{Code: "credentialRequired"}
		default:
			component.Reason = HealthReason{Code: "unknownState"}
		}
		return
	}
	if label == "" {
		return
	}
	state, err := m.serviceStateTarget(ctx, fmt.Sprintf("gui/%d/%s", m.Config.UID, label))
	checkedAt := m.Now().UTC()
	component.CheckKind, component.CheckedAt = "service", &checkedAt
	if err != nil {
		component.Reason = HealthReason{Code: "unknownState"}
		if errors.Is(err, os.ErrPermission) {
			component.Reason.Code = "permissionDenied"
			component.Authorization = "denied"
		}
		return
	}
	switch {
	case state.State == "running":
		component.Runtime = "running"
	case !state.Loaded || state.State == "not running" || state.State == "waiting":
		component.Runtime = "stopped"
	case state.State == "spawn scheduled":
		component.Runtime = "starting"
		component.Reason = HealthReason{Code: "starting", Retryable: true}
	}
}

func backendInventoryPath(path string, stat func(string) (os.FileInfo, error)) (os.FileInfo, error) {
	// A missing leaf beneath an unexpected symlink is not an authorized inventory
	// of this installation. CHATTY.md documents that health never follows these.
	current := string(filepath.Separator)
	var info os.FileInfo
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		var err error
		info, err = stat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, ReasonCode("invalidResponse")
		}
	}
	return info, nil
}

func installedVersionOutput(id ComponentID, output []byte) *string {
	if strings.ContainsAny(strings.TrimSpace(string(output)), "\r\n") {
		return nil
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return nil
	}
	switch id {
	case "tuwunel":
		if len(fields) == 2 && fields[0] == "tuwunel" {
			return versionNumber(fields[1])
		}
	case "postgres.whatsapp", "postgres.instagram":
		if len(fields) == 3 && fields[0] == "pg_isready" && fields[1] == "(PostgreSQL)" {
			return versionNumber(fields[2])
		}
	case "surrealdb":
		if fields[0] == "surreal" {
			return versionNumber(fields[1])
		}
	case "localai", "chattyBridges":
		name := "local-ai"
		if id == "chattyBridges" {
			name = "chatty-bridges"
		}
		if len(fields) == 3 && fields[0] == name && fields[1] == "version" {
			return versionNumber(strings.TrimPrefix(fields[2], "v"))
		}
	}
	return nil
}
