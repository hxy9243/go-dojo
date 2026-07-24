package rootfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	devTmpfsOptions = "mode=0755,size=16m"
	shmTmpfsOptions = "mode=1777,size=64m"
	runTmpfsOptions = "mode=0755,size=16m"
	stickyDirectory = os.ModeSticky | 0o777
)

type runtimeMounts struct {
	paths       []string
	cleanupOnce sync.Once
	cleanupErr  error
}

type filesystemMount struct {
	path   string
	mode   os.FileMode
	source string
	fsType string
	flags  uintptr
	data   string
}

// SetupRuntimeFilesystems creates and mounts the virtual filesystems needed by
// a minimal container. It must be called from the container's private mount
// namespace after the PID namespace has been created.
func SetupRuntimeFilesystems(rootfsDir string, cgroupPath string) (func() error, error) {
	rootfsDir, err := resolveRootfs(rootfsDir)
	if err != nil {
		return nil, err
	}

	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return nil, fmt.Errorf("make mounts private: %w", err)
	}

	mounts := &runtimeMounts{}
	if err := setupProc(rootfsDir, mounts); err != nil {
		return cleanupSetupFailure(mounts, err)
	}
	if err := setupDev(rootfsDir, mounts); err != nil {
		return cleanupSetupFailure(mounts, err)
	}
	if err := setupRun(rootfsDir, mounts); err != nil {
		return cleanupSetupFailure(mounts, err)
	}
	if err := setupCgroup(rootfsDir, cgroupPath, mounts); err != nil {
		return cleanupSetupFailure(mounts, err)
	}
	if err := setupRuntimeDirectories(rootfsDir); err != nil {
		return cleanupSetupFailure(mounts, err)
	}

	return mounts.cleanup, nil
}

// setupCgroup exposes only the container's cgroup leaf. The bind is read-only
// so the workload can inspect its limits and usage but cannot alter cgroup
// controls, create descendants, or move processes.
func setupCgroup(rootfsDir, cgroupPath string, mounts *runtimeMounts) error {
	if cgroupPath == "" {
		return nil
	}
	return mounts.bindMountReadOnly(rootfsDir, "/sys/fs/cgroup", cgroupPath, 0o755)
}

func cleanupSetupFailure(mounts *runtimeMounts, setupErr error) (func() error, error) {
	_ = mounts.cleanup()
	return nil, setupErr
}

func resolveRootfs(rootfsDir string) (string, error) {
	resolved, err := filepath.EvalSymlinks(rootfsDir)
	if err != nil {
		return "", fmt.Errorf("resolve rootfs: %w", err)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("make rootfs absolute: %w", err)
	}
	return absolute, nil
}

func setupProc(rootfsDir string, mounts *runtimeMounts) error {
	_, err := mounts.mount(rootfsDir, filesystemMount{
		path:   "/proc",
		mode:   0o555,
		source: "proc",
		fsType: "proc",
		flags:  unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC,
	})
	return err
}

func setupDev(rootfsDir string, mounts *runtimeMounts) error {
	devPath, err := mounts.mount(rootfsDir, filesystemMount{
		path:   "/dev",
		mode:   0o755,
		source: "tmpfs",
		fsType: "tmpfs",
		flags:  unix.MS_NOSUID,
		data:   devTmpfsOptions,
	})
	if err != nil {
		return err
	}
	if err := createDevices(devPath); err != nil {
		return err
	}
	if err := createDeviceLinks(devPath); err != nil {
		return err
	}
	_, err = mounts.mount(rootfsDir, filesystemMount{
		path:   "/dev/shm",
		mode:   stickyDirectory,
		source: "tmpfs",
		fsType: "tmpfs",
		flags:  unix.MS_NOSUID | unix.MS_NODEV,
		data:   shmTmpfsOptions,
	})
	return err
}

func setupRun(rootfsDir string, mounts *runtimeMounts) error {
	if _, err := mounts.mount(rootfsDir, filesystemMount{
		path:   "/run",
		mode:   0o755,
		source: "tmpfs",
		fsType: "tmpfs",
		flags:  unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC,
		data:   runTmpfsOptions,
	}); err != nil {
		return err
	}
	if _, err := ensureDirectory(rootfsDir, "/run/lock", stickyDirectory); err != nil {
		return fmt.Errorf("prepare /run/lock: %w", err)
	}
	return nil
}

func setupRuntimeDirectories(rootfsDir string) error {
	if _, err := ensureDirectory(rootfsDir, "/tmp", stickyDirectory); err != nil {
		return fmt.Errorf("prepare /tmp: %w", err)
	}
	return ensureVarRun(rootfsDir)
}

func (m *runtimeMounts) mount(rootfsDir string, spec filesystemMount) (string, error) {
	target, err := ensureDirectory(rootfsDir, spec.path, spec.mode)
	if err != nil {
		return "", fmt.Errorf("prepare %s: %w", spec.path, err)
	}
	if err := unix.Mount(spec.source, target, spec.fsType, spec.flags, spec.data); err != nil {
		return "", fmt.Errorf("mount %s: %w", spec.path, err)
	}
	m.paths = append(m.paths, target)
	return target, nil
}

func (m *runtimeMounts) bindMountReadOnly(rootfsDir, containerPath, source string, mode os.FileMode) error {
	target, err := ensureDirectory(rootfsDir, containerPath, mode)
	if err != nil {
		return fmt.Errorf("prepare %s: %w", containerPath, err)
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind mount %s: %w", containerPath, err)
	}
	m.paths = append(m.paths, target)
	// MS_BIND creates the mount but inherits the source's access flags. Remount
	// this bind specifically so it becomes read-only without remounting the
	// host cgroup filesystem itself.
	flags := uintptr(unix.MS_BIND | unix.MS_REMOUNT | unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	if err := unix.Mount("", target, "", flags, ""); err != nil {
		return fmt.Errorf("remount %s read-only: %w", containerPath, err)
	}
	return nil
}

func (m *runtimeMounts) cleanup() error {
	m.cleanupOnce.Do(func() {
		for i := len(m.paths) - 1; i >= 0; i-- {
			if err := unix.Unmount(m.paths[i], unix.MNT_DETACH); err != nil &&
				!errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
				m.cleanupErr = errors.Join(m.cleanupErr, fmt.Errorf("unmount %s: %w", m.paths[i], err))
			}
		}
	})
	return m.cleanupErr
}

// ensureDirectory creates containerPath beneath rootfsDir without following
// symlinks in any path component, then applies mode to the final directory.
func ensureDirectory(rootfsDir, containerPath string, mode os.FileMode) (string, error) {
	clean := filepath.Clean(containerPath)
	if !filepath.IsAbs(clean) || clean == "/" {
		return "", fmt.Errorf("invalid container directory %q", containerPath)
	}

	current := rootfsDir
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("%s is a symlink", current)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("%s is not a directory", current)
			}
		case os.IsNotExist(err):
			createMode := os.FileMode(0o755)
			if i == len(parts)-1 {
				createMode = mode
			}
			if err := os.Mkdir(current, createMode); err != nil {
				return "", err
			}
		default:
			return "", err
		}
	}
	if err := os.Chmod(current, mode); err != nil {
		return "", err
	}
	return current, nil
}

func createDevices(devPath string) error {
	devices := []struct {
		name       string
		majorMinor uint64
	}{
		{name: "null", majorMinor: unix.Mkdev(1, 3)},
		{name: "zero", majorMinor: unix.Mkdev(1, 5)},
		{name: "random", majorMinor: unix.Mkdev(1, 8)},
		{name: "urandom", majorMinor: unix.Mkdev(1, 9)},
	}
	for _, device := range devices {
		path := filepath.Join(devPath, device.name)
		if err := unix.Mknod(path, unix.S_IFCHR|0o666, int(device.majorMinor)); err != nil {
			return fmt.Errorf("create /dev/%s: %w", device.name, err)
		}
		if err := os.Chmod(path, 0o666); err != nil {
			return fmt.Errorf("chmod /dev/%s: %w", device.name, err)
		}
	}
	return nil
}

func createDeviceLinks(devPath string) error {
	links := map[string]string{
		"fd":     "/proc/self/fd",
		"stdin":  "/proc/self/fd/0",
		"stdout": "/proc/self/fd/1",
		"stderr": "/proc/self/fd/2",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(devPath, name)); err != nil {
			return fmt.Errorf("create /dev/%s: %w", name, err)
		}
	}
	return nil
}

func ensureVarRun(rootfsDir string) error {
	varPath, err := ensureDirectory(rootfsDir, "/var", 0o755)
	if err != nil {
		return fmt.Errorf("prepare /var: %w", err)
	}
	varRunPath := filepath.Join(varPath, "run")
	if _, err := os.Lstat(varRunPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect /var/run: %w", err)
	}
	if err := os.Symlink("../run", varRunPath); err != nil {
		return fmt.Errorf("create /var/run: %w", err)
	}
	return nil
}
