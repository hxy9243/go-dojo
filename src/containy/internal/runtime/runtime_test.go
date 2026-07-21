package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenPTY(t *testing.T) {
	master, slave, err := openPTY()
	if err != nil {
		t.Fatalf("openPTY() failed: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	if master == nil || slave == nil {
		t.Fatalf("expected non-nil master and slave PTY files")
	}

	if !strings.HasPrefix(slave.Name(), "/dev/pts/") {
		t.Errorf("expected slave path under /dev/pts/, got %s", slave.Name())
	}
}

func TestSetupRunDir(t *testing.T) {
	rootfsDir := t.TempDir()

	if err := setupResolvConf(rootfsDir); err != nil {
		t.Fatalf("setupResolvConf() failed: %v", err)
	}

	resolvConf := filepath.Join(rootfsDir, "/etc/resolv.conf")
	resolverConfig, err := os.ReadFile(resolvConf)
	if err != nil {
		t.Fatalf("read stub resolver configuration: %v", err)
	}
	if len(resolverConfig) == 0 {
		t.Fatal("expected non-empty stub resolver configuration")
	}
}

func TestRunWithOptions_Basic(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Skipping runtime test: requires root privileges")
	}

	tmpDir := t.TempDir()

	err := os.MkdirAll(filepath.Join(tmpDir, "bin"), 0755)
	if err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	shPath, err := os.Readlink("/bin/sh")
	if err == nil {
		_ = os.Symlink(shPath, filepath.Join(tmpDir, "bin", "sh"))
	}

	opts := Options{
		RootfsDir:   tmpDir,
		Command:     "/bin/sh",
		Args:        []string{"-c", "exit 0"},
		Interactive: false,
		TTY:         false,
	}

	err = RunWithOptions(opts)
	if err != nil {
		t.Fatalf("RunWithOptions failed: %v", err)
	}
}

func TestOptions_Flags(t *testing.T) {
	opts := Options{
		RootfsDir:   "/tmp",
		Command:     "echo",
		Args:        []string{"hello"},
		Interactive: true,
		TTY:         true,
	}

	if !opts.Interactive || !opts.TTY {
		t.Fatalf("expected Interactive and TTY to be true")
	}
}

func TestRunWithOptions_TTY(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Skipping runtime test: requires root privileges")
	}

	tmpDir := t.TempDir()

	err := os.MkdirAll(filepath.Join(tmpDir, "bin"), 0755)
	if err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	shPath, err := os.Readlink("/bin/sh")
	if err == nil {
		_ = os.Symlink(shPath, filepath.Join(tmpDir, "bin", "sh"))
	}

	opts := Options{
		RootfsDir:   tmpDir,
		Command:     "/bin/sh",
		Args:        []string{"-c", "exit 0"},
		Interactive: true,
		TTY:         true,
	}

	err = RunWithOptions(opts)
	if err != nil {
		t.Fatalf("RunWithOptions TTY failed: %v", err)
	}
}
