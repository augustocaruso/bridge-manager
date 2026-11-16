package chatty

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/urfave/cli/v2"
)

func assertEnglishHelp(t *testing.T, text string) {
	t.Helper()
	for _, character := range text {
		if character > unicode.MaxASCII {
			t.Fatalf("non-English help text: %s", text)
		}
	}
	legacy := regexp.MustCompile(`(?i)\b(gerenciar|registrar|submeter|instalar|renderizar|consultar|servir|gerar|inventariar|apagar|mostrar|publicar|diretorio|arquivo|somente|ponte|pontes|retorno|uso|apenas|sem|para)\b`)
	if legacy.MatchString(text) {
		t.Fatalf("legacy Portuguese help text: %s", text)
	}
}

func TestPublicCLIHelpEnglish(t *testing.T) {
	manager, _, _ := fixture(t)
	app := NewApp(manager)
	paths := [][]string{nil}
	var visit func([]string, []*cli.Command)
	visit = func(parent []string, commands []*cli.Command) {
		for _, command := range commands {
			path := append(append([]string(nil), parent...), command.Name)
			paths = append(paths, path)
			visit(path, command.Subcommands)
		}
	}
	visit(nil, app.Commands)
	for _, path := range paths {
		t.Run(strings.Join(append([]string{"chatty-bridges"}, path...), "/"), func(t *testing.T) {
			manager, out, runner := fixture(t)
			// Help must return before reading configuration or running an action.
			manager.Config.RuntimeRoot = filepath.Join(t.TempDir(), "absent")
			args := append(append([]string(nil), path...), "--help")
			if err := runApp(manager, args...); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "USAGE:") || len(runner.calls) != 0 {
				t.Fatalf("help did not render without subprocesses: %s", out.String())
			}
			assertEnglishHelp(t, out.String())
		})
	}
}

func TestPublicScriptHelpEnglish(t *testing.T) {
	for _, script := range []string{
		"deploy/macos/install-chatty-door-launchagent.sh",
		"deploy/macos/install-tailnet-https-redirect.sh",
		"deploy/macos/remove-tailnet-https-redirect.sh",
		"deploy/macos/stop-chatty-door-launchagent.sh",
		"deploy/macos/check-tailnet-door.sh",
		"cmd/chatty-bridges/build.sh",
	} {
		t.Run(script, func(t *testing.T) {
			command := exec.Command("bash", filepath.Join("..", "..", script), "--help")
			command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
			output, err := command.CombinedOutput()
			if script == "cmd/chatty-bridges/build.sh" {
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
					t.Fatalf("build usage should reject arguments: %v", err)
				}
			} else if err != nil {
				t.Fatalf("help failed: %v: %s", err, output)
			}
			if !strings.Contains(string(output), "Usage:") {
				t.Fatalf("missing usage: %s", output)
			}
			assertEnglishHelp(t, string(output))
		})
	}
}
