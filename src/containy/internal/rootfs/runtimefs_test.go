package rootfs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEnsureDirectoryRejectsSymlinkTraversal(t *testing.T) {
	rootfsDir := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(rootfsDir, "run")); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureDirectory(rootfsDir, "/run/lock", 0o1777); err == nil {
		t.Fatal("expected symlink traversal to be rejected")
	}
}

func TestEnsureDirectoryCreatesFinalMode(t *testing.T) {
	rootfsDir := t.TempDir()
	path, err := ensureDirectory(rootfsDir, "/run/lock", stickyDirectory)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o777 || info.Mode()&os.ModeSticky == 0 {
		t.Fatalf("mode = %v, want 1777", info.Mode())
	}
}

func TestEnsureVarRunPreservesExistingEntry(t *testing.T) {
	rootfsDir := t.TempDir()
	existing, err := ensureDirectory(rootfsDir, "/var/run", 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureVarRun(rootfsDir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(existing)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("/var/run mode = %v, want directory", info.Mode())
	}
}

func TestEnsureVarRunCreatesCompatibilitySymlink(t *testing.T) {
	rootfsDir := t.TempDir()
	if err := ensureVarRun(rootfsDir); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(rootfsDir, "var/run"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "../run" {
		t.Fatalf("/var/run target = %q, want ../run", target)
	}
}

func TestSetupRuntimeFilesystemsInPrivateNamespace(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("runtime filesystem mounts require root privileges")
	}
	rootfsDir := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestRuntimeFilesystemHelper$")
	command.Env = append(os.Environ(),
		"_CONTAINY_RUNTIMEFS_TEST=1",
		"_CONTAINY_RUNTIMEFS_ROOT="+rootfsDir,
	)
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS | syscall.CLONE_NEWPID,
	}
	output, err := command.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "operation not permitted") || errors.Is(err, syscall.EPERM) {
			t.Skipf("mount namespaces unavailable: %v: %s", err, output)
		}
		t.Fatalf("runtime filesystem helper failed: %v: %s", err, output)
	}

	var stat unix.Statfs_t
	if err := unix.Statfs(filepath.Join(rootfsDir, "proc"), &stat); err != nil {
		t.Fatal(err)
	}
	if uint64(stat.Type) == uint64(unix.PROC_SUPER_MAGIC) {
		t.Fatal("/proc mount leaked into the supervisor mount namespace")
	}
}

func TestRuntimeFilesystemHelper(t *testing.T) {
	if os.Getenv("_CONTAINY_RUNTIMEFS_TEST") != "1" {
		t.Skip("helper process only")
	}
	rootfsDir := os.Getenv("_CONTAINY_RUNTIMEFS_ROOT")
	cleanup, err := SetupRuntimeFilesystems(rootfsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	assertFilesystemType(t, filepath.Join(rootfsDir, "proc"), unix.PROC_SUPER_MAGIC)
	assertFilesystemType(t, filepath.Join(rootfsDir, "dev"), unix.TMPFS_MAGIC)
	assertFilesystemType(t, filepath.Join(rootfsDir, "dev/shm"), unix.TMPFS_MAGIC)
	assertFilesystemType(t, filepath.Join(rootfsDir, "run"), unix.TMPFS_MAGIC)

	if info, err := os.Stat(filepath.Join(rootfsDir, "run/lock")); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o777 || info.Mode()&os.ModeSticky == 0 {
		t.Fatalf("/run/lock mode = %v, want 1777", info.Mode())
	}

	var stat unix.Statfs_t
	sysPath := filepath.Join(rootfsDir, "sys")
	if err := unix.Statfs(sysPath, &stat); err == nil && uint64(stat.Type) == uint64(unix.SYSFS_MAGIC) {
		t.Fatal("/sys unexpectedly contains a sysfs mount")
	}
}

func assertFilesystemType(t *testing.T, path string, want int64) {
	t.Helper()
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		t.Fatal(err)
	}
	if uint64(stat.Type) != uint64(want) {
		t.Fatalf("%s filesystem type = %#x, want %#x", path, stat.Type, want)
	}
}
