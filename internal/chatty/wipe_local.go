package chatty

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type wipeLocalPath struct {
	Relative string
	Size     int64
	Exists   bool
}

func (m *Manager) wipeLocalPlan(b Bridge, dryRun bool) ([]wipeLocalPath, error) {
	config, err := readYAML(m.bridgePath(b, "config.native.yaml"))
	if err != nil {
		return nil, err
	}
	database := mapAt(config, "database")
	uri, ok := database["uri"].(string)
	if database["type"] != "postgres" || !ok {
		return nil, errors.New("wipe requires a native PostgreSQL database")
	}
	parsed, err := url.Parse(uri)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() || parsed.Path == "" || parsed.Path == "/" {
		return nil, errors.New("database does not match native loopback PostgreSQL")
	}
	paths := []wipeLocalPath{
		{Relative: filepath.Join("native/postgres", b.Name)},
		{Relative: filepath.Join("bridges", b.Name, "logs")},
		{Relative: filepath.Join("secrets", b.Name+"-login-events.json")},
		{Relative: filepath.Join("secrets", b.Name+"-login-qr-current.png")},
		{Relative: filepath.Join("secrets", b.Name+"-login-qr.png")},
	}
	for index := range paths {
		if dryRun && b.Name == "whatsapp" && index == 0 {
			paths[index].Size = -1
			continue
		}
		if err := m.inspectWipePath(&paths[index]); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func (m *Manager) inspectWipePath(path *wipeLocalPath) error {
	root := filepath.Clean(m.Config.RuntimeRoot)
	actual, err := filepath.EvalSymlinks(root)
	if err != nil || actual != root || !filepath.IsAbs(root) {
		return errors.New("runtime root missing or contains symlink")
	}
	parts := strings.Split(path.Relative, string(filepath.Separator))
	current := root
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errors.New("invalid local path")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("local state unavailable or contains symlink")
		}
	}
	path.Exists = true
	err = filepath.WalkDir(current, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot inventory local state")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in local state; wipe refused")
		}
		info, err := entry.Info()
		if err != nil {
			return errors.New("local state metadata unavailable")
		}
		if info.Mode().IsRegular() {
			path.Size += info.Size()
		} else if !info.IsDir() {
			return errors.New("special file in local state; wipe refused")
		}
		return nil
	})
	return err
}

func (m *Manager) requireWipeStopped(ctx context.Context, b Bridge) error {
	for _, component := range []string{"bridge", "postgres"} {
		state, err := m.serviceState(ctx, b, component)
		if err != nil {
			return err
		}
		if state.Loaded {
			return fmt.Errorf("%s/%s still loaded; stop and unload both services before wipe", b.Name, component)
		}
	}
	return nil
}

func (m *Manager) removeWipeLocal(ctx context.Context, b Bridge, paths []wipeLocalPath) error {
	if err := m.requireWipeStopped(ctx, b); err != nil {
		return err
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		inspected := wipeLocalPath{Relative: path.Relative}
		if err := m.inspectWipePath(&inspected); err != nil {
			return err
		}
		if !inspected.Exists {
			continue
		}
		root, err := os.OpenRoot(m.Config.RuntimeRoot)
		if err != nil {
			return errors.New("runtime root unavailable")
		}
		removeErr := root.RemoveAll(path.Relative)
		_, statErr := root.Lstat(path.Relative)
		closeErr := root.Close()
		if removeErr != nil || !os.IsNotExist(statErr) || closeErr != nil {
			return errors.New("local state removal unconfirmed; partial cleanup")
		}
		fmt.Fprintf(m.Out, "Local state removed: %s\n", filepath.Join(m.Config.RuntimeRoot, path.Relative))
	}
	return nil
}
