package chatty

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kballard/go-shellquote"
	"gopkg.in/yaml.v3"
)

type Credentials struct{ Username, Password, UserID string }

func (m *Manager) credentials() (Credentials, error) {
	data, err := os.ReadFile(filepath.Join(m.Config.RuntimeRoot, "secrets/first-user.txt"))
	if err != nil {
		return Credentials{}, errors.New("first-user.txt credentials unavailable")
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Credentials{}, errors.New("invalid credentials format")
		}
		if key != "username" && key != "password" && key != "user_id" {
			continue
		}
		parts, err := shellquote.Split(value)
		if err != nil || len(parts) != 1 {
			return Credentials{}, errors.New("invalid credentials format")
		}
		fields[key] = parts[0]
	}
	if fields["username"] == "" || fields["password"] == "" {
		return Credentials{}, errors.New("incomplete credentials")
	}
	return Credentials{fields["username"], fields["password"], fields["user_id"]}, nil
}

func readYAML(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("YAML file unavailable: " + filepath.Base(path))
	}
	var value map[string]any
	if err = yaml.Unmarshal(data, &value); err != nil || value == nil {
		return nil, errors.New("invalid YAML: " + filepath.Base(path))
	}
	return value, nil
}
func (m *Manager) registration(bridge Bridge) (map[string]any, error) {
	value, err := readYAML(m.bridgePath(bridge, "registration.native.yaml"))
	if err != nil {
		return nil, err
	}
	expectedURL := fmt.Sprintf("http://127.0.0.1:%d", bridge.Port)
	if value["id"] != bridge.Name || value["url"] != expectedURL {
		return nil, errors.New("registration does not match native bridge identity/address")
	}
	for _, key := range []string{"as_token", "hs_token", "sender_localpart"} {
		if token, ok := value[key].(string); !ok || token == "" {
			return nil, errors.New("incomplete registration")
		}
	}
	if _, ok := value["namespaces"].(map[string]any); !ok {
		return nil, errors.New("namespaces missing")
	}
	return value, nil
}

func (m *Manager) request(ctx context.Context, method, path, token string, payload, result any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return errors.New("invalid payload")
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(m.Config.Homeserver, "/")+path, body)
	if err != nil {
		return errors.New("invalid Matrix URL")
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := *m.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("Matrix request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("matrix HTTP %d", resp.StatusCode)
	}
	if result != nil {
		if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(result); err != nil {
			return errors.New("invalid Matrix response")
		}
	}
	return nil
}

func (m *Manager) Register(ctx context.Context, bridge Bridge, dryRun bool) (resultErr error) {
	if err := m.requireServerName(); err != nil {
		return err
	}
	if err := validateHomeserver(m.Config.Homeserver); err != nil {
		return err
	}
	registration, err := m.registration(bridge)
	if err != nil {
		return err
	}
	if dryRun {
		safe, err := yaml.Marshal(Redact(registration))
		if err != nil {
			return errors.New("registration redaction failed")
		}
		_, err = fmt.Fprintf(m.Out, "POST /_matrix/client/v3/login [credentials redacted]\nGET joined_rooms and canonical_alias (#admins:%s)\nPUT <admin-room>/send/m.room.message/<txn>\n!admin appservices register\n```yaml\n%s```\nConfirm response from @conduit:%s; POST logout. Nothing sent.\n", m.Config.ServerName, safe, m.Config.ServerName)
		return err
	}
	// Validate the operator-selected public origin before login or registration.
	// CHATTY.md documents this required configuration; dry-run stays offline.
	if _, err := validateDiscoveryBase(m.Config.PublicBaseURL); err != nil {
		return err
	}
	creds, err := m.credentials()
	if err != nil {
		return err
	}
	var login struct {
		AccessToken string `json:"access_token"`
	}
	err = m.request(ctx, http.MethodPost, "/_matrix/client/v3/login", "", map[string]any{"type": "m.login.password", "identifier": map[string]string{"type": "m.id.user", "user": creds.Username}, "password": creds.Password, "initial_device_display_name": "Chatty appservice registrar"}, &login)
	if err != nil {
		return err
	}
	if login.AccessToken == "" {
		return errors.New("login without access token")
	}
	defer func() {
		logoutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		logoutErr := m.request(logoutCtx, http.MethodPost, "/_matrix/client/v3/logout", login.AccessToken, map[string]any{}, nil)
		if resultErr == nil {
			resultErr = logoutErr
		}
	}()
	var joined struct {
		Rooms []string `json:"joined_rooms"`
	}
	if err = m.request(ctx, http.MethodGet, "/_matrix/client/v3/joined_rooms", login.AccessToken, nil, &joined); err != nil {
		return err
	}
	room := ""
	for _, candidate := range joined.Rooms {
		var alias struct {
			Alias string `json:"alias"`
		}
		err = m.request(ctx, http.MethodGet, "/_matrix/client/v3/rooms/"+url.PathEscape(candidate)+"/state/m.room.canonical_alias", login.AccessToken, nil, &alias)
		if err == nil && alias.Alias == "#admins:"+m.Config.ServerName {
			room = candidate
			break
		}
	}
	if room == "" {
		return errors.New("administrative room not found")
	}
	encoded, err := yaml.Marshal(registration)
	if err != nil {
		return errors.New("invalid registration")
	}
	since := m.Now().UnixMilli()
	txn := fmt.Sprintf("chatty-register-%s-%d", bridge.Name, m.Now().UnixNano())
	prefix := "/_matrix/client/v3/rooms/" + url.PathEscape(room)
	var sent struct {
		EventID string `json:"event_id"`
	}
	err = m.request(ctx, http.MethodPut, prefix+"/send/m.room.message/"+txn, login.AccessToken, map[string]string{"msgtype": "m.text", "body": "!admin appservices register\n```yaml\n" + string(encoded) + "```"}, &sent)
	if err != nil {
		return err
	}
	if sent.EventID == "" {
		return errors.New("registration sent without event_id")
	}
	for attempt := 0; attempt < 5; attempt++ {
		if err = m.Wait(ctx, time.Second); err != nil {
			return err
		}
		var messages struct {
			Chunk []struct {
				EventID   string `json:"event_id"`
				Sender    string `json:"sender"`
				Timestamp int64  `json:"origin_server_ts"`
				Content   struct {
					Body string `json:"body"`
				} `json:"content"`
			} `json:"chunk"`
		}
		if err = m.request(ctx, http.MethodGet, prefix+"/messages?dir=b&limit=16", login.AccessToken, nil, &messages); err != nil {
			return err
		}
		confirmed := false
		for _, event := range messages.Chunk {
			if event.EventID == sent.EventID {
				if confirmed {
					if err := m.persistRegistration(bridge); err != nil {
						return fmt.Errorf("registration confirmed on Tuwunel, but local marker was not persisted; do not repeat registration, repair only the marker: %w", err)
					}
					data, discoveryErr := m.wellKnown(m.Config.PublicBaseURL)
					if discoveryErr != nil {
						return fmt.Errorf("registration confirmed, but discovery failed; do not repeat registration: %w", discoveryErr)
					}
					if _, err = fmt.Fprintf(m.Out, "%s: registration confirmed on Tuwunel; local config preserved.\n", bridge.Name); err != nil {
						return err
					}
					_, err = m.Out.Write(data)
					return err
				}
				break
			}
			if event.Sender == "@conduit:"+m.Config.ServerName && event.Timestamp >= since && strings.TrimSpace(event.Content.Body) == "Appservice registered with ID: "+bridge.Name {
				confirmed = true
			}
		}
	}
	return errors.New("registration sent, but administrative confirmation not observed; do not repeat without checking state")
}

func (m *Manager) persistRegistration(bridge Bridge) error {
	path := m.bridgePath(bridge, ".registered")
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("marker must be a regular file without symlink")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return errors.New("marker unavailable")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".chatty-registration-*")
	if err != nil {
		return errors.New("cannot create marker temporary file")
	}
	defer os.Remove(file.Name())
	if _, err = file.WriteString("registered\n"); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot write marker")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return errors.New("cannot publish marker atomically")
	}
	return nil
}
