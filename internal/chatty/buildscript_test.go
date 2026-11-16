package chatty

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBuildScriptContracts(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"upstream-default", "upstream-output", "native", "native-staging", "native-machine", "native-empty-environment", "native-empty-file", "native-arguments", "native-failure"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			writeFixture(t, filepath.Join(bin, "go"), `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$BUILD_ARGS"
[ "${BUILD_FAIL:-0}" = 0 ] || exit 19
output=bbctl
previous=
for arg do
    if [ "$previous" = -o ]; then output=$arg; fi
    previous=$arg
    package=$arg
done
printf '%s\n' "$package" > "$output"
`)
			if err := os.Chmod(filepath.Join(bin, "go"), 0700); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(bin, "git"), "#!/bin/sh\nprintf 'synthetic-build-metadata\\n'\n")
			if err := os.Chmod(filepath.Join(bin, "git"), 0700); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(repo, "build.sh")
			var args []string
			expectedPackage := "github.com/beeper/bridge-manager/cmd/bbctl"
			outputPath := filepath.Join(root, "bbctl")
			if scenario == "upstream-output" {
				outputPath = filepath.Join(root, "custom-bbctl")
				args = []string{"-o", outputPath}
			}
			if strings.HasPrefix(scenario, "native") {
				script = filepath.Join(repo, "cmd/chatty-bridges/build.sh")
				expectedPackage = "github.com/beeper/bridge-manager/cmd/chatty-bridges"
			}
			if scenario == "native-arguments" {
				args = []string{"-o", filepath.Join(root, "override")}
			}
			cmd := exec.Command("/bin/sh", append([]string{script}, args...)...)
			cmd.Dir = root
			argsPath := filepath.Join(root, "args")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "CHATTY_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "HOME="+root, "PATH="+bin+":"+os.Getenv("PATH"), "BUILD_ARGS="+argsPath, "BUILD_FAIL=0")
			if scenario == "native-machine" {
				writeFixture(t, filepath.Join(root, "Library/Application Support/Chatty/machine.json"), `{"CHATTY_STAGING_ROOT": `+strconv.Quote(filepath.Join(root, "machine staging"))+`}`)
			}
			if scenario == "native-staging" {
				cmd.Env = append(cmd.Env, "CHATTY_STAGING_ROOT="+filepath.Join(root, "custom staging"))
			}
			if scenario == "native-empty-environment" {
				cmd.Env = append(cmd.Env, "CHATTY_STAGING_ROOT=")
			}
			if scenario == "native-empty-file" {
				writeFixture(t, filepath.Join(root, "Library/Application Support/Chatty/machine.json"), `{"CHATTY_STAGING_ROOT":""}`)
			}
			if scenario == "native-failure" {
				cmd.Env = append(cmd.Env, "BUILD_FAIL=1")
			}
			stdout, err := cmd.Output()
			if strings.HasPrefix(scenario, "native-empty-") {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || !strings.Contains(string(exit.Stderr), "CHATTY_STAGING_ROOT is set but empty") || len(stdout) != 0 {
					t.Fatalf("empty staging did not fail clearly: %v %q", err, stdout)
				}
				if _, err := os.Stat(argsPath); !os.IsNotExist(err) {
					t.Fatal("empty staging invoked go")
				}
				return
			}
			if scenario == "native-arguments" || scenario == "native-failure" {
				if err == nil || len(stdout) != 0 {
					t.Fatalf("invalid build reported success: %q, %v", stdout, err)
				}
				if scenario == "native-arguments" {
					if _, err := os.Stat(argsPath); !os.IsNotExist(err) {
						t.Fatal("invalid arguments invoked go")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "native" || scenario == "native-staging" || scenario == "native-machine" {
				outputPath = strings.TrimSpace(string(stdout))
				staging := filepath.Join(root, "Library/Caches/Chatty/bridge-manager")
				if scenario == "native-staging" {
					staging = filepath.Join(root, "custom staging")
				}
				if scenario == "native-machine" {
					staging = filepath.Join(root, "machine staging")
				}
				if !strings.HasPrefix(outputPath, staging+string(os.PathSeparator)) || filepath.Base(outputPath) != "chatty-bridges" {
					t.Fatalf("output outside native staging: %q", outputPath)
				}
			}
			product, err := os.ReadFile(outputPath)
			if err != nil || strings.TrimSpace(string(product)) != expectedPackage {
				t.Fatalf("wrong product or missing output: %q, %v", product, err)
			}
			buildArgs, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(buildArgs), "-p\n2\n") {
				t.Fatal("build parallelism not bounded")
			}
			if !strings.HasPrefix(scenario, "native") {
				for _, field := range []string{"main.Tag=", "main.Commit=", "main.BuildTime="} {
					if !strings.Contains(string(buildArgs), field) {
						t.Errorf("missing upstream metadata: %s", field)
					}
				}
			}
		})
	}
}
