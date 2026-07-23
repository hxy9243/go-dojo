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
func TestRunWithOptions_DirectoryMount(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Skipping runtime test: requires root privileges")
	}

	tmpDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmpDir, "bin"), 0755)
	shPath, err := os.Readlink("/bin/sh")
	if err == nil {
		_ = os.Symlink(shPath, filepath.Join(tmpDir, "bin", "sh"))
	}

	hostDir := t.TempDir()
	sampleFile := filepath.Join(hostDir, "hello.txt")
	if err := os.WriteFile(sampleFile, []byte("hello host"), 0644); err != nil {
		t.Fatalf("write sample file: %v", err)
	}

	opts := Options{
		RootfsDir: tmpDir,
		Command:   "/bin/sh",
		Args:      []string{"-c", "test \"$(cat /mnt/host/hello.txt)\" = \"hello host\""},
		Mounts: []Mount{
			{Source: hostDir, Destination: "/mnt/host", ReadOnly: false},
		},
	}

	if err := RunWithOptions(opts); err != nil {
		t.Fatalf("directory mount test failed: %v", err)
	}
}

func TestRunWithOptions_FileMount(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Skipping runtime test: requires root privileges")
	}

	tmpDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmpDir, "bin"), 0755)
	shPath, err := os.Readlink("/bin/sh")
	if err == nil {
		_ = os.Symlink(shPath, filepath.Join(tmpDir, "bin", "sh"))
	}

	hostFile := filepath.Join(t.TempDir(), "config.txt")
	if err := os.WriteFile(hostFile, []byte("key=value"), 0644); err != nil {
		t.Fatalf("write host file: %v", err)
	}

	opts := Options{
		RootfsDir: tmpDir,
		Command:   "/bin/sh",
		Args:      []string{"-c", "test \"$(cat /etc/app/config.txt)\" = \"key=value\""},
		Mounts: []Mount{
			{Source: hostFile, Destination: "/etc/app/config.txt", ReadOnly: false},
		},
	}

	if err := RunWithOptions(opts); err != nil {
		t.Fatalf("file mount test failed: %v", err)
	}
}

func TestRunWithOptions_ReadOnlyMount(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Skipping runtime test: requires root privileges")
	}

	tmpDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmpDir, "bin"), 0755)
	shPath, err := os.Readlink("/bin/sh")
	if err == nil {
		_ = os.Symlink(shPath, filepath.Join(tmpDir, "bin", "sh"))
	}

	hostDir := t.TempDir()

	opts := Options{
		RootfsDir: tmpDir,
		Command:   "/bin/sh",
		Args:      []string{"-c", "touch /mnt/ro/newfile"},
		Mounts: []Mount{
			{Source: hostDir, Destination: "/mnt/ro", ReadOnly: true},
		},
	}

	err = RunWithOptions(opts)
	if err == nil {
		t.Fatal("expected error when writing to read-only bind mount, got nil")
	}
}
