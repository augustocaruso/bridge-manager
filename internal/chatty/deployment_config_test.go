package chatty

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func deploymentEnvironment(t *testing.T) []string {
	t.Helper()
	var environment []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CHATTY_") {
			environment = append(environment, entry)
		}
	}
	return append(environment,
		"HOME="+canonicalTempDir(t),
		"CHATTY_RUNTIME_ROOT=/synthetic/runtime & <test>",
		"CHATTY_HOSTNAME=node.example.ts.net",
		"CHATTY_MATRIX_UPSTREAM=http://127.0.0.1:18009",
		"CHATTY_SERVER_NAME=matrix.example.org",
		"CHATTY_TAILNET_IPV6_PREFIX=fd00:1234:5678",
		"CHATTY_BINARY=/synthetic/bin/bridges & test",
		"CHATTY_CERT_DIR=/synthetic/private/certs",
		"CHATTY_WELLKNOWN_FILE=/synthetic/public/discovery.json",
		"CHATTY_HTTP_LISTEN_IPV4=127.0.0.1:18008",
		"CHATTY_HTTP_LISTEN_IPV6=[::1]:18008",
		"CHATTY_BACKEND_CONFIG=/synthetic/private/health.json")
}

func deploymentCommand(t *testing.T, environment []string, script string, args ...string) *exec.Cmd {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("../../deploy/macos", script))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/bash", append([]string{path}, args...)...)
	command.Env = environment
	command.Dir = t.TempDir()
	return command
}

func TestDeploymentRendersConfiguredArguments(t *testing.T) {
	for _, optional := range []bool{false, true} {
		t.Run(map[bool]string{false: "TLS-only", true: "HTTP-and-health"}[optional], func(t *testing.T) {
			environment := deploymentEnvironment(t)
			if !optional {
				environment = environment[:len(environment)-3]
			}
			command := deploymentCommand(t, environment, "install-chatty-door-launchagent.sh", "--render", "100.64.0.10", "fd00:1234:5678::10", "/synthetic/tailscale & <cli>")
			data, err := command.Output()
			if err != nil {
				t.Fatal(err)
			}
			type plistValue struct {
				XMLName xml.Name
				Text    string   `xml:",chardata"`
				Strings []string `xml:"string"`
			}
			var document struct {
				Dict struct {
					Entries []plistValue `xml:",any"`
				} `xml:"dict"`
			}
			if err := xml.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			entries := document.Dict.Entries
			if len(entries)%2 != 0 {
				t.Fatal("plist dictionary has unmatched keys/values")
			}
			values := map[string]plistValue{}
			for index := 0; index < len(entries); index += 2 {
				if entries[index].XMLName.Local != "key" {
					t.Fatal("plist dictionary entry has no key")
				}
				values[entries[index].Text] = entries[index+1]
			}
			want := []string{"/synthetic/bin/bridges & test", "--runtime-root", "/synthetic/runtime & <test>", "--launchd-prefix", "local.chatty", "--server-name", "matrix.example.org", "door",
				"--listen", "100.64.0.10:8448", "--listen", "[fd00:1234:5678::10]:8448",
				"--hostname", "node.example.ts.net", "--cert-dir", "/synthetic/private/certs",
				"--matrix-upstream", "http://127.0.0.1:18009", "--wellknown-file", "/synthetic/public/discovery.json",
				"--tailscale-bin", "/synthetic/tailscale & <cli>"}
			if optional {
				want = append(want, "--http-listen", "127.0.0.1:18008", "--http-listen", "[::1]:18008", "--backend-config", "/synthetic/private/health.json")
			}
			if !reflect.DeepEqual(values["ProgramArguments"].Strings, want) || values["WorkingDirectory"].Text != "/synthetic/runtime & <test>" || values["StandardOutPath"].Text != "/synthetic/runtime & <test>/logs/chatty-door.out.log" {
				t.Fatalf("wrong rendered configuration: %+v", values)
			}
			files, err := os.ReadDir(command.Dir)
			if err != nil || len(files) != 0 {
				t.Fatal("render wrote files")
			}
		})
	}
}

func TestDeploymentMissingConfiguration(t *testing.T) {
	for _, missing := range []string{"CHATTY_RUNTIME_ROOT", "CHATTY_HOSTNAME", "CHATTY_MATRIX_UPSTREAM", "CHATTY_SERVER_NAME", "CHATTY_TAILNET_IPV6_PREFIX"} {
		t.Run(missing, func(t *testing.T) {
			environment := deploymentEnvironment(t)
			for index, entry := range environment {
				if strings.HasPrefix(entry, missing+"=") {
					environment[index] = missing + "="
				}
			}
			command := deploymentCommand(t, environment, "install-chatty-door-launchagent.sh", "--render", "100.64.0.10", "fd00:1234:5678::10", "/synthetic/tailscale")
			data, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(data), "Missing "+missing) || strings.Contains(string(data), "<plist") {
				t.Fatalf("missing value did not fail clearly: %v %s", err, data)
			}
		})
	}
	command := deploymentCommand(t, nil, "check-tailnet-door.sh")
	command.Env = []string{"PATH=/nonexistent", "HOME=" + canonicalTempDir(t)}
	data, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(data), "CHATTY_CERTIFICATE") || strings.Contains(string(data), "launchctl") {
		t.Fatalf("check queried services without configuration: %v %s", err, data)
	}
}

func TestRedirectConfiguredPrefixAndLiteralValidation(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		args  []string
		valid bool
	}{
		{"compressed", []string{"100.64.0.10", "fd00:1234:5678::10", "fd00:1234:5678"}, true},
		{"full", []string{"100.127.255.255", "fd00:1234:5678:0:0:0:0:10", "fd00:1234:5678"}, true},
		{"missing-prefix", []string{"100.64.0.10", "fd00:1234:5678::10"}, false},
		{"wrong-prefix", []string{"100.64.0.10", "fd00:9999:5678::10", "fd00:1234:5678"}, false},
		{"bad-prefix", []string{"100.64.0.10", "fd00:1234:5678::10", "fd00:1234:5678;unsafe"}, false},
		{"non-tailnet", []string{"192.0.2.10", "fd00:1234:5678::10", "fd00:1234:5678"}, false},
		{"bad-octet", []string{"100.64.0.256", "fd00:1234:5678::10", "fd00:1234:5678"}, false},
		{"leading-zero", []string{"100.064.0.10", "fd00:1234:5678::10", "fd00:1234:5678"}, false},
		{"bad-IPv6", []string{"100.64.0.10", "fd00:1234:5678:::10", "fd00:1234:5678"}, false},
		{"zone", []string{"100.64.0.10", "fd00:1234:5678::10%utun0", "fd00:1234:5678"}, false},
		{"injection", []string{"100.64.0.10\npass all", "fd00:1234:5678::10", "fd00:1234:5678"}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			command := deploymentCommand(t, deploymentEnvironment(t), "install-tailnet-https-redirect.sh", append([]string{"--render"}, scenario.args...)...)
			data, err := command.Output()
			if (err == nil) != scenario.valid {
				t.Fatalf("valid=%t: %v %s", scenario.valid, err, data)
			}
			if scenario.valid {
				want := "rdr pass inet proto tcp from any to " + scenario.args[0] + " port 443 -> " + scenario.args[0] + " port 8448\n" +
					"rdr pass inet6 proto tcp from any to " + scenario.args[1] + " port 443 -> " + scenario.args[1] + " port 8448\n"
				if string(data) != want {
					t.Fatalf("wrong redirect: %s", data)
				}
			} else if len(data) != 0 {
				t.Fatal("invalid values emitted rules")
			}
		})
	}
}
