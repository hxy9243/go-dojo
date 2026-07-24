package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReservedRuntimeDestination(t *testing.T) {
	tests := map[string]bool{
		"/":                      true,
		"/proc":                  true,
		"/proc/self":             true,
		"/dev/null":              true,
		"/run/lock":              true,
		"/sys/fs/cgroup":         true,
		"/sys/fs/cgroup/cpu.max": true,
		"/tmp":                   true,
		"/var":                   true,
		"/tmp/cache":             false,
		"/var/lib":               false,
		"/procedure":             false,
		"/device":                false,
		"/runtime":               false,
		"/sys":                   false,
		"/mnt/volume":            false,
	}
	for destination, want := range tests {
		if got := reservedRuntimeDestination(destination); got != want {
			t.Errorf("reservedRuntimeDestination(%q) = %v, want %v", destination, got, want)
		}
	}
}

func TestApplyMountsRejectsReservedDestinationBeforeMount(t *testing.T) {
	source := t.TempDir()
	for _, destination := range []string{"/", "/proc", "/dev/shm", "/run/lock", "/sys/fs/cgroup", "/tmp", "/var"} {
		_, err := applyMounts(t.TempDir(), []Mount{{Source: source, Destination: destination}})
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("destination %q error = %v, want reserved-path error", destination, err)
		}
	}
}

func TestApplyMountsRejectsSymlinkDestinationBeforeMount(t *testing.T) {
	rootfsDir := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(rootfsDir, "data")); err != nil {
		t.Fatal(err)
	}
	_, err := applyMounts(rootfsDir, []Mount{{
		Source:      t.TempDir(),
		Destination: "/data/nested",
	}})
	if err == nil || !strings.Contains(err.Error(), "traverses symlink") {
		t.Fatalf("error = %v, want symlink traversal error", err)
	}
}

func TestRelaySignalsForwardsToWorkloadProcessGroup(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "trap 'exit 0' TERM; echo ready; while :; do :; done")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("workload readiness = %q, %v", line, err)
	}

	signals := make(chan os.Signal, 1)
	done := relaySignals(command.Process.Pid, signals)
	signals <- syscall.SIGTERM

	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case err := <-waitDone:
		close(done)
		if err != nil {
			t.Fatalf("command did not handle forwarded signal: %v", err)
		}
	case <-time.After(time.Second):
		close(done)
		_ = unix.Kill(-command.Process.Pid, syscall.SIGKILL)
		t.Fatal("forwarded signal did not reach workload process group")
	}
}

func TestBootstrapFileRoundTrip(t *testing.T) {
	want := initConfig{
		RootfsDir:  "/tmp/rootfs",
		CgroupPath: "/sys/fs/cgroup/containy/test",
		Hostname:   "containy-test",
		Command:    "/bin/sh",
		Args:       []string{"-c", "exit 0"},
	}
	file, err := createBootstrapFile(want)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var got initConfig
	if err := json.NewDecoder(file).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.RootfsDir != want.RootfsDir || got.CgroupPath != want.CgroupPath || got.Hostname != want.Hostname || got.Command != want.Command ||
		strings.Join(got.Args, "\x00") != strings.Join(want.Args, "\x00") {
		t.Fatalf("bootstrap = %+v, want %+v", got, want)
	}
}

func TestCreateInitCommandIncludesCgroupNamespaceAndPath(t *testing.T) {
	opts := Options{RootfsDir: "/tmp/rootfs", Command: "/bin/sh"}
	cmd, bootstrap, err := createInitCommand(opts, "containy-test", 0, "/sys/fs/cgroup/containy/test")
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()

	if cmd.SysProcAttr.Cloneflags&syscall.CLONE_NEWCGROUP == 0 {
		t.Fatal("private init does not create a cgroup namespace")
	}
	var config initConfig
	if err := json.NewDecoder(bootstrap).Decode(&config); err != nil {
		t.Fatal(err)
	}
	if config.CgroupPath != "/sys/fs/cgroup/containy/test" {
		t.Fatalf("cgroup path = %q", config.CgroupPath)
	}
}

func TestReadInitConfig(t *testing.T) {
	data, err := json.Marshal(initConfig{
		RootfsDir:  "/tmp/rootfs",
		CgroupPath: "/sys/fs/cgroup/containy/test",
		Hostname:   "containy-test",
		Command:    "/bin/sh",
		Args:       []string{"-c", "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fd := bootstrapTestFD(t, data)
	config, err := readInitConfig(fd)
	if err != nil {
		t.Fatal(err)
	}
	if config.RootfsDir != "/tmp/rootfs" || config.CgroupPath != "/sys/fs/cgroup/containy/test" || config.Hostname != "containy-test" || config.Command != "/bin/sh" || len(config.Args) != 2 {
		t.Fatalf("config = %+v", config)
	}
	if err := unix.Close(fd); err != unix.EBADF {
		t.Fatalf("bootstrap descriptor was not closed: %v", err)
	}
}

func TestReadInitConfigRejectsInvalidAndOversizedPayloads(t *testing.T) {
	tests := []struct {
		name        string
		data        []byte
		errContains string
	}{
		{
			name:        "missing command",
			data:        []byte(`{"rootfs":"/tmp/rootfs"}`),
			errContains: "invalid bootstrap configuration",
		},
		{
			name:        "malformed JSON",
			data:        []byte(`{"rootfs":`),
			errContains: "decode bootstrap",
		},
		{
			name:        "oversized",
			data:        bytes.Repeat([]byte("x"), maxBootstrap+1),
			errContains: "bootstrap exceeds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := readInitConfig(bootstrapTestFD(t, test.data))
			if err == nil || !strings.Contains(err.Error(), test.errContains) {
				t.Fatalf("error = %v, want %q", err, test.errContains)
			}
		})
	}
}

func bootstrapTestFD(t *testing.T, data []byte) int {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "bootstrap-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Dup(int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

func TestSetupResolvConfNormalizesRunSymlink(t *testing.T) {
	rootfsDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootfsDir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(rootfsDir, "etc/resolv.conf")
	if err := os.Symlink("/run/systemd/resolve/resolv.conf", target); err != nil {
		t.Fatal(err)
	}
	if err := setupResolvConf(rootfsDir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("/etc/resolv.conf mode = %v, want regular file", info.Mode())
	}
}
