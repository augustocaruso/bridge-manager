package chatty

import (
	"errors"

	"github.com/urfave/cli/v2"
)

func bridgeCommand(name, usage string, flags func() []cli.Flag, action func(*cli.Context, Bridge) error) *cli.Command {
	command := &cli.Command{Name: name, Usage: usage, Action: func(*cli.Context) error { return errors.New("supply a bridge: instagram or whatsapp") }}
	for _, name := range []string{"instagram", "whatsapp"} {
		bridge, _ := Lookup(name)
		command.Subcommands = append(command.Subcommands, &cli.Command{Name: name, Flags: flags(), Action: func(ctx *cli.Context) error {
			if ctx.NArg() != 0 {
				return errors.New("extra arguments are not allowed")
			}
			return action(ctx, bridge)
		}})
	}
	return command
}

func NewApp(manager *Manager) *cli.App {
	noFlags := func() []cli.Flag { return nil }
	app := &cli.App{Name: "chatty-bridges", Usage: "Manage native Chatty bridges on Tuwunel", Version: "0.1.0", Writer: manager.Out, ErrWriter: manager.Out}
	app.Flags = []cli.Flag{
		&cli.StringFlag{Name: "launchd-prefix", Value: manager.Config.LaunchdPrefix, Usage: "LaunchAgent label prefix"},
		&cli.StringFlag{Name: "homeserver", Value: manager.Config.Homeserver, Usage: "Administrative Matrix HTTP loopback origin"},
		&cli.StringFlag{Name: "server-name", Value: manager.Config.ServerName, Usage: "Matrix server_name (no default); required for config, register, wipe and health, including door --backend-config"},
		&cli.StringFlag{Name: "runtime-root", Value: manager.Config.RuntimeRoot, Usage: "Native installation runtime directory"},
		&cli.StringFlag{Name: "server-repo", Value: manager.Config.ServerRepo, Usage: "Server checkout containing scripts/build-mautrix-native.sh"},
		&cli.StringFlag{Name: "staging-root", Value: manager.Config.StagingRoot, Usage: "Exclusive build staging directory"},
		&cli.StringFlag{Name: "build-queue", Value: manager.Config.QueueSubmit, Usage: "Build queue submit executable"},
		&cli.StringFlag{Name: "public-base-url", Value: manager.Config.PublicBaseURL, Usage: "Public HTTPS origin for registration discovery (no default)"},
	}
	app.Before = func(ctx *cli.Context) error {
		manager.Config.LaunchdPrefix = ctx.String("launchd-prefix")
		if manager.Config.LaunchdPrefix == "" {
			manager.Config.LaunchdPrefix = DefaultLaunchdPrefix
		}
		if !launchdPrefixPattern.MatchString(manager.Config.LaunchdPrefix) {
			return errors.New("--launchd-prefix must contain only ASCII letters, digits, dots and hyphens and start with a letter or digit")
		}
		manager.Config.Homeserver = ctx.String("homeserver")
		manager.Config.ServerName = ctx.String("server-name")
		manager.Config.RuntimeRoot = ctx.String("runtime-root")
		manager.Config.ServerRepo = ctx.String("server-repo")
		manager.Config.StagingRoot = ctx.String("staging-root")
		manager.Config.QueueSubmit = ctx.String("build-queue")
		manager.Config.PublicBaseURL = ctx.String("public-base-url")
		return nil
	}
	app.Commands = []*cli.Command{
		backendHealthCommand(manager),
		wellKnownCommand(manager),
		doorCommand(manager),
		wipeCommand(manager),
		bridgeCommand("register", "Register an appservice in the administrative room", func() []cli.Flag {
			return []cli.Flag{&cli.BoolFlag{Name: "dry-run", Usage: "Show a redacted plan without network access or writes"}}
		}, func(ctx *cli.Context, b Bridge) error { return manager.Register(ctx.Context, b, ctx.Bool("dry-run")) }),
		bridgeCommand("build", "Submit a native build to the queue without installing", func() []cli.Flag {
			return []cli.Flag{&cli.StringFlag{Name: "tag", Required: true}, &cli.StringFlag{Name: "card", EnvVars: []string{"CHATTY_BUILD_CARD"}}}
		}, func(ctx *cli.Context, b Bridge) error {
			return manager.Build(ctx.Context, b, ctx.String("tag"), ctx.String("card"))
		}),
		bridgeCommand("install", "Install a staged binary with a backup, without starting the service", func() []cli.Flag {
			return []cli.Flag{&cli.StringFlag{Name: "source", Required: true, Usage: "Source directory produced by the build"}}
		}, func(ctx *cli.Context, b Bridge) error { return manager.Install(ctx.Context, b, ctx.String("source")) }),
		bridgeCommand("config", "Render redacted configuration or compare its structure with the live configuration", func() []cli.Flag { return []cli.Flag{&cli.BoolFlag{Name: "diff"}} }, func(ctx *cli.Context, b Bridge) error { return manager.RenderConfig(b, ctx.Bool("diff")) }),
		bridgeCommand("status", "Query services, health and versions (read-only)", noFlags, func(ctx *cli.Context, b Bridge) error { return manager.Status(ctx.Context, b) }),
	}
	service := &cli.Command{Name: "service", Usage: "Query or control existing LaunchAgents", Action: func(*cli.Context) error { return errors.New("supply status, start or stop and a bridge") }}
	for _, action := range []string{"status", "start", "stop"} {
		service.Subcommands = append(service.Subcommands, bridgeCommand(action, "PostgreSQL and bridge LaunchAgents", noFlags, func(ctx *cli.Context, b Bridge) error { return manager.Service(ctx.Context, b, action) }))
	}
	app.Commands = append(app.Commands, service)
	return app
}
