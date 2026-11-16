package chatty

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"

	"github.com/urfave/cli/v2"
)

type WipeOptions struct {
	DryRun            bool
	Confirm           string
	RemoveBridgeUsers bool
}

func wipeCommand(m *Manager) *cli.Command {
	return bridgeCommand("wipe", "Inventory or remove a bridge's rooms and state", func() []cli.Flag {
		return []cli.Flag{
			&cli.BoolFlag{Name: "dry-run", Usage: "Read-only inventory using GET; does not change rooms, users or files"},
			&cli.StringFlag{Name: "confirm", Usage: "Exact room count from the approved dry run; required for deletion"},
			&cli.BoolFlag{Name: "remove-bridge-users", Usage: "Permanent removal: deactivate + erase; MXIDs remain reserved and CANNOT be reused"},
		}
	}, func(ctx *cli.Context, b Bridge) error {
		if ctx.Bool("dry-run") && ctx.IsSet("confirm") {
			return errors.New("--dry-run does not accept --confirm")
		}
		return m.Wipe(ctx.Context, b, WipeOptions{DryRun: ctx.Bool("dry-run"), Confirm: ctx.String("confirm"), RemoveBridgeUsers: ctx.Bool("remove-bridge-users")})
	})
}

func (m *Manager) Wipe(ctx context.Context, b Bridge, options WipeOptions) error {
	if err := m.requireServerName(); err != nil {
		return err
	}
	verified, err := Lookup(b.Name)
	if err != nil || verified != b {
		return errors.New("invalid bridge")
	}
	count := -1
	if !options.DryRun {
		count, err = strconv.Atoi(options.Confirm)
		if err != nil || count < 0 || strconv.Itoa(count) != options.Confirm {
			return errors.New("deletion requires --confirm <count> from the approved dry run")
		}
	} else if options.Confirm != "" {
		return errors.New("--dry-run does not accept --confirm")
	}
	if err := validateHomeserver(m.Config.Homeserver); err != nil {
		return err
	}
	if !options.DryRun {
		if err := m.requireWipeStopped(ctx, b); err != nil {
			return err
		}
	}
	identity, err := m.wipeIdentity(b)
	if err != nil {
		return err
	}
	token, owner, err := m.wipeSession(ctx)
	if err != nil {
		return err
	}
	identity.owner = owner
	plan, err := m.wipeInventory(ctx, b, identity, token)
	if err != nil {
		return err
	}
	paths, err := m.wipeLocalPlan(b, options.DryRun)
	if err != nil {
		return err
	}
	m.printWipePlan(b, plan, paths, options)
	if options.DryRun {
		return nil
	}
	if count != len(plan.Rooms) {
		return errors.New("--confirm differs from current inventory; nothing deleted; repeat and approve the dry run")
	}
	if options.RemoveBridgeUsers && len(plan.Mixed) > 0 {
		return errors.New("mixed rooms exist; permanent user removal requires manual review before continuing")
	}
	if err := m.requireWipeStopped(ctx, b); err != nil {
		return err
	}
	for _, room := range plan.Rooms {
		if err := m.requireWipeStopped(ctx, b); err != nil {
			return err
		}
		kind, err := m.wipeRoomKind(ctx, b, identity, plan.Protected, token, room.ID)
		if err != nil {
			return err
		}
		if kind != room.Kind {
			return errors.New("room changed since inventory; cleanup stopped, repeat the dry run")
		}
		var result struct {
			Failed *[]string `json:"failed_to_kick_users"`
		}
		path := "/_synapse/admin/v1/rooms/" + url.PathEscape(room.ID)
		if err := m.request(ctx, http.MethodDelete, path, token, map[string]bool{"purge": true, "block": false}, &result); err != nil {
			return err
		}
		if result.Failed == nil || len(*result.Failed) != 0 {
			return errors.New("purge did not confirm users were kicked; cleanup stopped")
		}
		remaining, err := m.wipeRooms(ctx, token)
		if err != nil {
			return err
		}
		for _, id := range remaining {
			if id == room.ID {
				return errors.New("room still present after purge; local state preserved")
			}
		}
		fmt.Fprintf(m.Out, "Room removed and absence confirmed: %s\n", room.ID)
	}
	if options.RemoveBridgeUsers {
		for _, user := range plan.Users {
			if user.Deactivated && user.Erased != nil && *user.Erased {
				continue
			}
			if err := m.requireWipeStopped(ctx, b); err != nil {
				return err
			}
			if err := m.request(ctx, http.MethodPost, "/_synapse/admin/v1/deactivate/"+url.PathEscape(user.Name), token, map[string]bool{"erase": true}, nil); err != nil {
				return err
			}
			users, err := m.wipeUsers(ctx, token)
			if err != nil {
				return err
			}
			erased := false
			for _, current := range users {
				if current.Name == user.Name && current.Deactivated && current.Erased != nil && *current.Erased {
					erased = true
				}
			}
			if !erased {
				return errors.New("deactivation and erase unconfirmed; local state preserved")
			}
		}
	}
	if err := m.removeWipeLocal(ctx, b, paths); err != nil {
		return err
	}
	fmt.Fprintln(m.Out, "Cleanup complete; config, registration, binaries and other bridges preserved. No service started.")
	return nil
}

func (m *Manager) printWipePlan(b Bridge, plan wipePlan, paths []wipeLocalPath, options WipeOptions) {
	fmt.Fprintf(m.Out, "Wipe %s: inventory of IDs and counts only.\nCandidate rooms: %d\n", b.Name, len(plan.Rooms))
	for _, room := range plan.Rooms {
		fmt.Fprintf(m.Out, "%s %s\n", room.Kind, room.ID)
	}
	fmt.Fprintf(m.Out, "MIXED, review manually (excluded from automatic deletion): %d\n", len(plan.Mixed))
	for _, room := range plan.Mixed {
		fmt.Fprintln(m.Out, room)
	}
	fmt.Fprintf(m.Out, "Rooms kept without bridge association: %d (includes personal rooms and other bridges, such as Instagram when cleaning WhatsApp).\n", plan.Kept)
	if options.RemoveBridgeUsers {
		fmt.Fprintf(m.Out, "WARNING: deactivate + erase bridge users: %d; MXIDs remain reserved, incompatible with reimport.\n", len(plan.Users))
	} else {
		fmt.Fprintf(m.Out, "Bridge users kept (bot/namespace): %d; reusable for reimport.\n", len(plan.Users))
	}
	for index, path := range paths {
		label := "Local state"
		if index == 0 {
			label = "Database"
		}
		size := fmt.Sprintf("%d bytes; exists=%t", path.Size, path.Exists)
		if path.Size < 0 {
			size = "size not measured by policy; WhatsApp PostgreSQL directory not inspected"
		}
		fmt.Fprintf(m.Out, "%s: %s (%s)\n", label, filepath.Join(m.Config.RuntimeRoot, path.Relative), size)
	}
	fmt.Fprintln(m.Out, "Kept: config, registration, PostgreSQL/double-puppet secrets, binaries, plists, other bridges and users outside the namespace.")
	fmt.Fprintf(m.Out, "After approving the exact list and unloading both services: wipe %s --confirm %d", b.Name, len(plan.Rooms))
	if options.RemoveBridgeUsers {
		fmt.Fprint(m.Out, " --remove-bridge-users")
	}
	fmt.Fprintln(m.Out, ". The count does not prove list identity; changes require a new dry run and approval.")
}
