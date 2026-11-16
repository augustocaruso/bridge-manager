package chatty

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const NativeTag = "v0.2610.0"

var jobPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,100}$`)

func diskAvailable(path string) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return errors.New("cannot measure free disk space")
	}
	if stat.Bavail*uint64(stat.Bsize) < 2<<30 {
		return errors.New("disk below the 2 GiB anti-ENOSPC floor")
	}
	return nil
}
func (m *Manager) Build(ctx context.Context, b Bridge, tag, card string) error {
	if strings.TrimSpace(m.Config.QueueSubmit) == "" {
		return errors.New("missing --build-queue; configure a queue submit executable before building")
	}
	if tag != NativeTag {
		return errors.New("native builder supports only " + NativeTag + "; no job submitted")
	}
	if !jobPattern.MatchString(card) {
		return errors.New("supply a valid --card or CHATTY_BUILD_CARD")
	}
	if err := os.MkdirAll(m.Config.StagingRoot, 0700); err != nil {
		return errors.New("cannot create staging directory")
	}
	if err := diskAvailable(m.Config.StagingRoot); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(m.Config.StagingRoot, b.Name+"-")
	if err != nil {
		return errors.New("cannot reserve exclusive staging directory")
	}
	source := filepath.Join(stage, "source")
	output, err := m.Runner.Run(ctx, m.Config.QueueSubmit, "--card", card, "--label", "bridge-manager "+b.Name+" "+tag, "--", "/usr/bin/env", "GOMAXPROCS=2", "CARGO_BUILD_JOBS=4", "/bin/bash", filepath.Join(m.Config.ServerRepo, "scripts/build-mautrix-native.sh"), b.Name, source)
	if err != nil {
		return errors.New("build submission failed; staging preserved: " + stage)
	}
	job := strings.TrimSpace(string(output))
	if !jobPattern.MatchString(job) {
		return errors.New("queue did not return a valid job ID; check the queue before retrying")
	}
	fmt.Fprintf(m.Out, "job=%s\nstaging=%s\ntag=%s\nNothing installed. Query the configured queue: %q --result %s.\n", job, source, tag, m.Config.QueueSubmit, job)
	return nil
}

func confinedDirectory(root, path string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", errors.New("invalid staging root")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", errors.New("invalid source")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("source must be inside staging")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("staging unavailable")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("source unavailable")
	}
	if resolvedRoot != root || resolved != path {
		return "", errors.New("symlinks are not allowed in staging")
	}
	return path, nil
}
func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("regular file required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("file unavailable")
	}
	return data, nil
}
func copyExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return errors.New("cannot create exclusive file")
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}
func (m *Manager) Install(ctx context.Context, b Bridge, source string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := confinedDirectory(m.Config.StagingRoot, source)
	if err != nil {
		return err
	}
	name := "mautrix-" + b.Name
	data, err := readRegular(filepath.Join(source, name))
	if err != nil {
		return errors.New("staging binary unavailable or not regular")
	}
	receipt, err := readRegular(filepath.Join(source, "receipt.txt"))
	if err != nil {
		return errors.New("receipt.txt unavailable")
	}
	sum := sha256.Sum256(data)
	expected := hex.EncodeToString(sum[:]) + "  " + name
	found := false
	binarySection := false
	tagFound := false
	for _, line := range strings.Split(string(receipt), "\n") {
		if line == "tag="+NativeTag {
			tagFound = true
		}
		if line == "binary (SHA-256):" {
			binarySection = true
			continue
		}
		if binarySection {
			found = line == expected
			break
		}
	}
	if !found || !tagFound {
		return errors.New("receipt does not confirm binary tag/hash")
	}
	target := m.binaryPath(b)
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil || parent != filepath.Dir(target) {
		return errors.New("installation directory missing or contains symlink")
	}
	if err = diskAvailable(parent); err != nil {
		return err
	}
	var previous []byte
	if info, statErr := os.Lstat(target); statErr == nil {
		if !info.Mode().IsRegular() {
			return errors.New("destination is not a regular file")
		}
		previous, err = readRegular(target)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(statErr) {
		return errors.New("destination unavailable")
	}
	temp, err := os.CreateTemp(parent, ".chatty-install-*")
	if err != nil {
		return errors.New("cannot create installation candidate")
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err = temp.Chmod(0755); err != nil {
		temp.Close()
		return errors.New("candidate permission change failed")
	}
	_, writeErr := temp.Write(data)
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if errors.Join(writeErr, syncErr, closeErr) != nil {
		return errors.New("candidate write failed")
	}
	backup := ""
	if previous != nil {
		backup = target + ".pre-" + m.Now().UTC().Format("20060102T150405.000000000Z")
		if err = copyExclusive(backup, previous, 0755); err != nil {
			return errors.New("exclusive backup failed; destination preserved")
		}
	}
	if err = os.Rename(tempName, target); err != nil {
		return errors.New("atomic replacement failed; backup preserved")
	}
	directory, err := os.Open(parent)
	if err != nil {
		return errors.New("installed, but directory could not be synced")
	}
	syncErr = directory.Sync()
	closeErr = directory.Close()
	if errors.Join(syncErr, closeErr) != nil {
		return errors.New("installed, but directory sync failed")
	}
	fmt.Fprintf(m.Out, "%s: binary installed; backup=%s. No service started.\n", b.Name, backup)
	return nil
}
