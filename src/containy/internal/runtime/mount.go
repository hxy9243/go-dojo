package runtime

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Mount represents a host directory or file bind mount into the container.
type Mount struct {
	Source      string // Absolute path on the host
	Destination string // Absolute path inside the container
	ReadOnly    bool   // Mount as read-only if true
}

// applyMounts validates and applies bind mounts onto hostTarget locations inside rootfsDir.
// It returns a slice of successfully mounted host target paths (in order) or an error.
// If any mount operation fails, any mounts applied in the current call are unmounted.
func applyMounts(rootfsDir string, mounts []Mount) ([]string, error) {
	var applied []string

	for _, m := range mounts {
		srcInfo, err := os.Stat(m.Source)
		if err != nil {
			cleanupMounts(applied)
			return nil, fmt.Errorf("mount source %q: %w", m.Source, err)
		}

		cleanDest := filepath.Clean(m.Destination)
		if !filepath.IsAbs(cleanDest) {
			cleanupMounts(applied)
			return nil, fmt.Errorf("mount destination %q must be an absolute path", m.Destination)
		}

		hostTarget := filepath.Join(rootfsDir, cleanDest)

		if srcInfo.IsDir() {
			if targetInfo, err := os.Stat(hostTarget); err == nil && !targetInfo.IsDir() {
				cleanupMounts(applied)
				return nil, fmt.Errorf("cannot mount directory %q onto file target %q", m.Source, hostTarget)
			}
			if err := os.MkdirAll(hostTarget, 0755); err != nil {
				cleanupMounts(applied)
				return nil, fmt.Errorf("mkdir target dir %q: %w", hostTarget, err)
			}
		} else {
			if targetInfo, err := os.Stat(hostTarget); err == nil && targetInfo.IsDir() {
				cleanupMounts(applied)
				return nil, fmt.Errorf("cannot mount file %q onto directory target %q", m.Source, hostTarget)
			}
			if err := os.MkdirAll(filepath.Dir(hostTarget), 0755); err != nil {
				cleanupMounts(applied)
				return nil, fmt.Errorf("mkdir parent dir for file %q: %w", hostTarget, err)
			}
			if _, err := os.Stat(hostTarget); os.IsNotExist(err) {
				f, err := os.OpenFile(hostTarget, os.O_CREATE|os.O_WRONLY, 0644)
				if err != nil {
					cleanupMounts(applied)
					return nil, fmt.Errorf("create file target %q: %w", hostTarget, err)
				}
				_ = f.Close()
			}
		}

		if err := unix.Mount(m.Source, hostTarget, "", unix.MS_BIND, ""); err != nil {
			cleanupMounts(applied)
			return nil, fmt.Errorf("bind mount %q -> %q: %w", m.Source, hostTarget, err)
		}
		applied = append(applied, hostTarget)

		if m.ReadOnly {
			if err := unix.Mount("", hostTarget, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
				cleanupMounts(applied)
				return nil, fmt.Errorf("remount ro %q: %w", hostTarget, err)
			}
		}
	}

	return applied, nil
}

// cleanupMounts unmounts all applied mount targets in reverse order.
func cleanupMounts(applied []string) {
	for i := len(applied) - 1; i >= 0; i-- {
		_ = unix.Unmount(applied[i], unix.MNT_DETACH)
	}
}
