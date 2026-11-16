package chatty

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func machineShell(t *testing.T, environment []string, action string, args ...string) ([]byte, error) {
	t.Helper()
	loader, err := filepath.Abs("../../deploy/macos/chatty-machine.sh")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", append([]string{"-c", `. "$1"; shift; ` + action, "machine-test", loader}, args...)...)
	command.Env = environment
	return command.CombinedOutput()
}

func TestMachineReadersRejectInvalidSchema(t *testing.T) {
	home := isolatedMachineHome(t)
	path := filepath.Join(home, "Library/Application Support/Chatty/machine.json")
	for _, body := range []string{
		`{"CHATTY_LAUNCHD_PREFX":"org.example"}`,
		`{"CHATTY_QUEUE_SUBMIT":"/synthetic/submit"}`,
		`{"CHATTY_UNKNOWN":42}`,
		`{},"settings":{"CHATTY_LAUNCHD_PREFIX":"org.injected"}`,
		`{"":"empty key"}`,
		`{"CHATTY_STAGING_ROOT":"/valid","":"empty key"}`,
		`{"CHATTY_STAGING_ROOT":42}`,
		`{"CHATTY_STAGING_ROOT":null}`,
		`{"CHATTY_RUNTIME_ROOT":{}}`,
		`<plist version="1.0"><dict><key>CHATTY_LAUNCHD_PREFIX</key><string>org.example</string></dict></plist>`,
	} {
		t.Run(body, func(t *testing.T) {
			t.Setenv("CHATTY_STAGING_ROOT", "/synthetic/environment")
			writeFixture(t, path, body)
			if _, err := DefaultConfig(); err == nil {
				t.Error("Go reader accepted invalid machine configuration")
			}
			output, err := machineShell(t, os.Environ(), `printf 'ACCEPTED\n'`)
			if err == nil || strings.Contains(string(output), "ACCEPTED") || !strings.Contains(string(output), "CHATTY_MACHINE_CONFIG") {
				t.Fatalf("shell reader did not fail clearly: %v %s", err, output)
			}
		})
	}
}

func TestMachineReaderKnownKeys(t *testing.T) {
	home := isolatedMachineHome(t)
	path := filepath.Join(home, "Library/Application Support/Chatty/machine.json")
	settings := map[string]string{}
	for _, key := range machineSettingKeys {
		settings[key] = "synthetic-" + key
	}
	settings["CHATTY_LAUNCHD_PREFIX"] = "org.example"
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, string(data))
	got, err := machineSettings(home)
	if err != nil || len(got) != len(settings) {
		t.Fatalf("known Go keys: %v %+v", err, got)
	}
	for key, value := range settings {
		output, err := machineShell(t, os.Environ(), `/usr/bin/printenv "$1"`, key)
		if err != nil || string(output) != value+"\n" || got[key] != value {
			t.Fatalf("reader mismatch for %s: %v %q", key, err, output)
		}
	}
	writeFixture(t, path, `{}`)
	if output, err := machineShell(t, os.Environ(), `printf '%s' "$CHATTY_LAUNCHD_PREFIX"`); err != nil || string(output) != DefaultLaunchdPrefix {
		t.Fatalf("empty object: %v %s", err, output)
	}
}

func TestMachineReadersPreserveStringValues(t *testing.T) {
	home := isolatedMachineHome(t)
	path := filepath.Join(home, "Library/Application Support/Chatty/machine.json")
	value := "/synthetic/a & <b> \"quoted\"\n\n"
	data, err := json.Marshal(map[string]string{"CHATTY_STAGING_ROOT": value})
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, " \n\t"+string(data))
	settings, err := machineSettings(home)
	if err != nil || settings["CHATTY_STAGING_ROOT"] != value {
		t.Fatalf("Go string changed: %v %+v", err, settings)
	}
	output, err := machineShell(t, os.Environ(), `printf '%s' "$CHATTY_STAGING_ROOT"`)
	if err != nil || string(output) != value {
		t.Fatalf("shell string changed: %v %q", err, output)
	}
}

func TestMachineReadErrorIncludesPathAndCause(t *testing.T) {
	home := isolatedMachineHome(t)
	path := filepath.Join(home, "missing.json")
	t.Setenv("CHATTY_MACHINE_CONFIG", path)
	_, err := DefaultConfig()
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), path) {
		t.Fatalf("missing path/cause: %v", err)
	}
}

func TestMachineShellRequiresHomeOrExplicitFile(t *testing.T) {
	isolatedMachineHome(t)
	for _, value := range []string{"", "relative"} {
		output, err := machineShell(t, []string{"PATH=/usr/bin:/bin", "HOME=" + value}, `printf 'ACCEPTED\n'`)
		if err == nil || !strings.Contains(string(output), "HOME") {
			t.Fatalf("invalid home accepted: %v %s", err, output)
		}
	}
}

func TestRedirectRequiresExplicitMachineSelection(t *testing.T) {
	isolatedMachineHome(t)
	for _, mode := range []struct {
		script string
		args   []string
	}{
		{"install-tailnet-https-redirect.sh", []string{"--install", "100.64.0.10", "fd00:1234:5678::10", "fd00:1234:5678"}},
		{"install-tailnet-https-redirect.sh", []string{"--load"}},
		{"remove-tailnet-https-redirect.sh", nil},
	} {
		output, err := deploymentCommand(t, os.Environ(), mode.script, mode.args...).CombinedOutput()
		if err == nil || !strings.Contains(string(output), "explicit") || !strings.Contains(string(output), "CHATTY_") {
			t.Fatalf("%s accepted ambiguous privileged selection: %v %s", mode.script, err, output)
		}
	}
}

func TestRedirectPureModesIgnoreMachineFile(t *testing.T) {
	isolatedMachineHome(t)
	environment := append(os.Environ(), "CHATTY_MACHINE_CONFIG=/synthetic/missing.json")
	for _, mode := range []struct {
		script string
		args   []string
	}{
		{"install-tailnet-https-redirect.sh", []string{"--help"}},
		{"install-chatty-door-launchagent.sh", []string{"--help"}},
		{"stop-chatty-door-launchagent.sh", []string{"--help"}},
		{"check-tailnet-door.sh", []string{"--help"}},
		{"install-tailnet-https-redirect.sh", []string{"--render", "100.64.0.10", "fd00:1234:5678::10", "fd00:1234:5678"}},
		{"remove-tailnet-https-redirect.sh", []string{"--help"}},
	} {
		output, err := deploymentCommand(t, environment, mode.script, mode.args...).CombinedOutput()
		if err != nil {
			t.Fatalf("pure mode read machine file: %v %s", err, output)
		}
	}
}

func TestRedirectSingletonOwnership(t *testing.T) {
	isolatedMachineHome(t)
	for _, scenario := range []struct {
		name     string
		files    []string
		required string
		valid    bool
	}{
		{"new", nil, "optional", true},
		{"missing-removal", nil, "required", false},
		{"same", []string{"org.example-tailnet-redirect.plist"}, "required", true},
		{"other", []string{"org.other-tailnet-redirect.plist"}, "optional", false},
		{"both", []string{"org.example-tailnet-redirect.plist", "org.other-tailnet-redirect.plist"}, "required", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := canonicalTempDir(t)
			for _, file := range scenario.files {
				writeFixture(t, filepath.Join(root, file), "synthetic")
			}
			output, err := machineShell(t, os.Environ(), `chatty_redirect_owner "$1" org.example-tailnet-redirect "$2"`, root, scenario.required)
			if (err == nil) != scenario.valid {
				t.Fatalf("ownership valid=%t: %v %s", scenario.valid, err, output)
			}
			if !scenario.valid && !strings.Contains(string(output), "redirect") {
				t.Fatalf("unclear ownership refusal: %s", output)
			}
		})
	}
}
