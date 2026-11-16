package chatty

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentMachineFileAndPrefix(t *testing.T) {
	home := isolatedMachineHome(t)
	settings := map[string]string{}
	for _, entry := range deploymentEnvironment(t) {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CHATTY_") {
			settings[key] = value
		}
	}
	settings["CHATTY_LAUNCHD_PREFIX"] = "org.example.machine"
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(home, "Library/Application Support/Chatty/machine.json"), string(data))
	for _, override := range []bool{false, true} {
		environment := os.Environ()
		prefix := "org.example.machine"
		if override {
			prefix = "org.environment"
			environment = append(environment, "CHATTY_LAUNCHD_PREFIX="+prefix)
		}
		command := deploymentCommand(t, environment, "install-chatty-door-launchagent.sh", "--render", "100.64.0.10", "fd00:1234:5678::10", "/synthetic/tailscale")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("machine render: %s %v", output, err)
		}
		for _, expected := range []string{"<string>" + prefix + "-door</string>", "<string>--launchd-prefix</string>", "<string>" + prefix + "</string>", "/synthetic/runtime &amp; &lt;test&gt;", "<string>--backend-config</string>", "<string>--http-listen</string>"} {
			if !strings.Contains(string(output), expected) {
				t.Fatalf("missing %s in %s", expected, output)
			}
		}
		command = deploymentCommand(t, environment, "install-tailnet-https-redirect.sh", "--render-plist")
		output, err = command.CombinedOutput()
		if err != nil {
			t.Fatalf("redirect render: %s %v", output, err)
		}
		for _, expected := range []string{"<string>" + prefix + "-tailnet-redirect</string>", "<key>CHATTY_LAUNCHD_PREFIX</key><string>" + prefix + "</string>", "/Library/PrivilegedHelperTools/" + prefix + "-tailnet-redirect.sh", "/var/log/" + prefix + "-tailnet-redirect.out.log"} {
			if !strings.Contains(string(output), expected) {
				t.Fatalf("missing %s in %s", expected, output)
			}
		}
	}
}

func TestDeploymentRejectsInvalidMachineConfiguration(t *testing.T) {
	home := isolatedMachineHome(t)
	path := filepath.Join(home, "Library/Application Support/Chatty/machine.json")
	for _, body := range []string{"{", "null", "[]", `{"CHATTY_LAUNCHD_PREFIX":"../unsafe"}`, `{"CHATTY_STAGING_ROOT":null}`, `{"CHATTY_STAGING_ROOT":42}`} {
		writeFixture(t, path, body)
		for _, script := range []string{"install-chatty-door-launchagent.sh", "stop-chatty-door-launchagent.sh", "check-tailnet-door.sh", "install-tailnet-https-redirect.sh", "remove-tailnet-https-redirect.sh"} {
			var args []string
			if script == "install-chatty-door-launchagent.sh" {
				args = []string{"--render", "100.64.0.10", "fd00:1234:5678::10", "/synthetic/tailscale"}
			}
			if script == "install-tailnet-https-redirect.sh" {
				args = []string{"--render-plist"}
			}
			if script == "remove-tailnet-https-redirect.sh" {
				args = nil
			}
			command := deploymentCommand(t, append(os.Environ(), "CHATTY_MACHINE_CONFIG="+path), script, args...)
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "CHATTY_") {
				t.Fatalf("invalid configuration accepted by %s: %v %s", script, err, output)
			}
		}
	}
}

func TestRootLoaderDoesNotReadUserMachineFile(t *testing.T) {
	isolatedMachineHome(t)
	environment := append(os.Environ(), "CHATTY_MACHINE_CONFIG=/synthetic/missing/machine.json", "CHATTY_LAUNCHD_PREFIX=org.frozen")
	command := deploymentCommand(t, environment, "install-tailnet-https-redirect.sh", "--load", "unexpected")
	output, err := command.CombinedOutput()
	if err == nil || string(output) != "Extra arguments.\n" {
		t.Fatalf("root loader read user settings: %v %s", err, output)
	}
}
