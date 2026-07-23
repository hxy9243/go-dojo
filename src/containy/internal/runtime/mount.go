package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Mount represents a host directory or file bind mount into the container.
type Mount struct {
	Source      string
	Destination string
	ReadOnly    bool
}

// applyMounts validates and applies bind mounts onto targets inside rootfsDir.
func applyMounts(rootfsDir string, mounts []Mount) ([]string, error) {
	var applied []string
	fail := func(err error) ([]string, error) {
		cleanupMounts(applied)
		return nil, err
	}

	for _, mount := range mounts {
		sourceInfo, err := os.Stat(mount.Source)
		if err != nil {
			return fail(fmt.Errorf("mount source %q: %w", mount.Source, err))
		}

		destination := filepath.Clean(mount.Destination)
		if !filepath.IsAbs(destination) {
			return fail(fmt.Errorf("mount destination %q must be an absolute path", mount.Destination))
		}
		if reservedRuntimeDestination(destination) {
			return fail(fmt.Errorf("mount destination %q is reserved for a runtime filesystem", mount.Destination))
		}

		target, err := resolveMountTarget(rootfsDir, destination)
		if err != nil {
			return fail(err)
		}
		if err := prepareMountTarget(target, mount.Source, sourceInfo.IsDir()); err != nil {
			return fail(err)
		}
		if err := unix.Mount(mount.Source, target, "", unix.MS_BIND, ""); err != nil {
			return fail(fmt.Errorf("bind mount %q -> %q: %w", mount.Source, target, err))
		}
		applied = append(applied, target)

		if mount.ReadOnly {
			if err := unix.Mount("", target, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
				return fail(fmt.Errorf("remount ro %q: %w", target, err))
			}
		}
	}
	return applied, nil
}

// resolveMountTarget rejects existing symlinks between rootfsDir and the
// destination. This prevents a path such as /data/link/config from resolving
// outside the rootfs while the supervisor is mounting as root.
func resolveMountTarget(rootfsDir, destination string) (string, error) {
	current := rootfsDir
	for _, component := range strings.Split(strings.TrimPrefix(destination, "/"), "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			return "", fmt.Errorf("mount destination %q traverses symlink %q", destination, current)
		case err == nil:
			continue
		case os.IsNotExist(err):
			return filepath.Join(rootfsDir, destination), nil
		default:
			return "", fmt.Errorf("inspect mount destination %q: %w", current, err)
		}
	}
	return filepath.Join(rootfsDir, destination), nil
}

func prepareMountTarget(target, source string, sourceIsDir bool) error {
	targetInfo, targetErr := os.Stat(target)
	if sourceIsDir {
		if targetErr == nil && !targetInfo.IsDir() {
			return fmt.Errorf("cannot mount directory %q onto file target %q", source, target)
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("mkdir target dir %q: %w", target, err)
		}
		return nil
	}

	if targetErr == nil && targetInfo.IsDir() {
		return fmt.Errorf("cannot mount file %q onto directory target %q", source, target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("mkdir parent dir for file %q: %w", target, err)
	}
	if os.IsNotExist(targetErr) {
		file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("create file target %q: %w", target, err)
		}
		return file.Close()
	}
	return targetErr
}

func reservedRuntimeDestination(destination string) bool {
	if destination == "/" || destination == "/tmp" || destination == "/var" {
		return true
	}
	for _, reserved := range []string{"/proc", "/dev", "/run"} {
		if destination == reserved || strings.HasPrefix(destination, reserved+"/") {
			return true
		}
	}
	return false
}

func cleanupMounts(applied []string) {
	for i := len(applied) - 1; i >= 0; i-- {
		_ = unix.Unmount(applied[i], unix.MNT_DETACH)
	}
}
