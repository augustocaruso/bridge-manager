package chatty

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Homeserver    string
	ServerName    string
	RuntimeRoot   string
	ServerRepo    string
	StagingRoot   string
	QueueSubmit   string
	PublicBaseURL string
	LaunchdPrefix string
	Home          string
	UID           int
}

func DefaultConfig() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, errors.New("cannot locate HOME")
	}
	settings, err := machineSettings(home)
	if err != nil {
		return Config{}, err
	}
	value := func(key, fallback string) string {
		if setting, ok := os.LookupEnv(key); ok {
			return setting
		}
		if setting, ok := settings[key]; ok {
			return setting
		}
		return fallback
	}
	support := filepath.Join(home, "Library/Application Support/Chatty")
	return Config{
		Homeserver:    value("CHATTY_HOMESERVER", "http://127.0.0.1:8008"),
		ServerName:    value("CHATTY_SERVER_NAME", ""),
		RuntimeRoot:   value("CHATTY_RUNTIME_ROOT", filepath.Join(support, "runtime")),
		ServerRepo:    value("CHATTY_SERVER_REPO", filepath.Join(support, "matrix-server")),
		StagingRoot:   value("CHATTY_STAGING_ROOT", filepath.Join(home, "Library/Caches/Chatty/bridge-manager")),
		QueueSubmit:   value("CHATTY_BUILD_QUEUE", ""),
		PublicBaseURL: value("CHATTY_PUBLIC_BASE_URL", ""),
		LaunchdPrefix: value("CHATTY_LAUNCHD_PREFIX", DefaultLaunchdPrefix),
		Home:          home, UID: os.Getuid(),
	}, nil
}

type Bridge struct {
	Name string
	Port int
}

func Lookup(name string) (Bridge, error) {
	switch name {
	case "instagram":
		return Bridge{name, 29320}, nil
	case "whatsapp":
		return Bridge{name, 29318}, nil
	default:
		return Bridge{}, errors.New("unsupported bridge; use instagram or whatsapp")
	}
}

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type ProcessError struct{ Code int }

func (err *ProcessError) Error() string { return "external process failed" }

type ExecRunner struct{}

type observationError struct {
	message string
	cause   error
}

func (err *observationError) Error() string { return err.message }
func (err *observationError) Unwrap() error { return err.cause }

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, &ProcessError{Code: exit.ExitCode()}
		}
		return nil, &observationError{message: "process unavailable: " + filepath.Base(name), cause: err}
	}
	return output, nil
}

type Manager struct {
	Config             Config
	HTTP               *http.Client
	Runner             Runner
	Out                io.Writer
	Now                func() time.Time
	Wait               func(context.Context, time.Duration) error
	Lstat              func(string) (os.FileInfo, error)
	Backend            *BackendConfig
	BackendHTTPSOrigin string
}

func New(config Config, out io.Writer) *Manager {
	return &Manager{Config: config, Out: out, HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Runner: ExecRunner{}, Now: time.Now, Wait: func(ctx context.Context, duration time.Duration) error {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}}
}
func (m *Manager) requireServerName() error {
	// Identity-dependent commands fail before effects instead of inventing an
	// identity. CHATTY.md documents --server-name; discovery alone does not need it.
	if strings.TrimSpace(m.Config.ServerName) == "" {
		return errors.New("missing --server-name; supply the Matrix server_name before the command")
	}
	return nil
}
func (m *Manager) bridgePath(bridge Bridge, name string) string {
	return filepath.Join(m.Config.RuntimeRoot, "bridges", bridge.Name, name)
}
func (m *Manager) binaryPath(bridge Bridge) string {
	return filepath.Join(m.Config.RuntimeRoot, "native/bin", "mautrix-"+bridge.Name)
}
