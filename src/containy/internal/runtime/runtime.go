// Package runtime implements a host supervisor and private init bootstrap using
// Linux mount, PID, UTS, and IPC namespaces, cgroups v2, and chroot.
//
// This package is Linux-only and requires root privileges.
package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/hxy9243/go-dojo/containy/internal/cgroup"
	"github.com/hxy9243/go-dojo/containy/internal/rootfs"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	initMarkerEnv = "_CONTAINY_INTERNAL_INIT=1"

	// bootstrapFD is the file descriptor from which the re-executed process
	// reads its init configuration. File descriptors 0, 1, and 2 are reserved
	// for standard input, output, and error. os/exec assigns ExtraFiles[0] to
	// the next descriptor, 3, in the child process.
	bootstrapFD = 3

	// maxBootstrap bounds the configuration accepted from the supervisor.
	// Besides limiting memory use, the bound rejects truncated or unexpectedly
	// large bootstrap documents before they are decoded.
	maxBootstrap   = 64 << 10 // 64 KiB
	defaultPidsMax = 256
	signalGrace    = 2 * time.Second
)

var forwardedSignals = []os.Signal{
	syscall.SIGINT,
	syscall.SIGTERM,
	syscall.SIGHUP,
	syscall.SIGQUIT,
	syscall.SIGUSR1,
	syscall.SIGUSR2,
}

// Options configures container execution.
type Options struct {
	RootfsDir   string
	Command     string
	Args        []string
	Interactive bool // -i: keep stdin open
	TTY         bool // -t: allocate a pseudo-TTY
	Mounts      []Mount
	Limits      cgroup.Limits
}

// initConfig is the private bootstrap payload passed from the host supervisor
// to the re-executed container init process. It is sent through a file
// descriptor instead of command-line arguments or environment variables so
// those implementation details are not exposed to the container workload.
type initConfig struct {
	RootfsDir string   `json:"rootfs"`
	Hostname  string   `json:"hostname"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
}

func init() {
	if os.Getenv("_CONTAINY_INTERNAL_INIT") != "1" {
		return
	}
	if err := runContainerInit(bootstrapFD); err != nil {
		fmt.Fprintf(os.Stderr, "containy: init: %v\n", err)
		os.Exit(125)
	}
	panic("containy init returned after exec")
}

// Run executes command with inherited stdio inside rootfsDir.
func Run(rootfsDir string, command string, args ...string) error {
	return RunWithOptions(Options{
		RootfsDir:   rootfsDir,
		Command:     command,
		Args:        args,
		Interactive: true,
		TTY:         false,
	})
}

// RunWithOptions creates the container namespaces and cgroup, then executes the
// command through the private init bootstrap.
func RunWithOptions(opts Options) error {
	if err := validateOptions(&opts); err != nil {
		return err
	}
	rootfsDir, err := resolveRootfsDir(opts.RootfsDir)
	if err != nil {
		return err
	}
	opts.RootfsDir = rootfsDir
	containerID, err := newContainerID()
	if err != nil {
		return err
	}

	cleanupHostMounts, err := prepareHostFilesystem(opts)
	if err != nil {
		return err
	}
	defer cleanupHostMounts()

	group, err := createContainerCgroup(containerID, opts.Limits)
	if err != nil {
		return err
	}

	cmd, bootstrap, err := createInitCommand(opts, containerID, group.FD())
	if err != nil {
		_ = group.Cleanup()
		return err
	}
	defer bootstrap.Close()

	runErr := executeInitCommand(cmd, opts)
	finishCgroup(group)
	return runErr
}

func validateOptions(opts *Options) error {
	if os.Getuid() != 0 {
		return errors.New("containy requires root privileges (run with sudo)")
	}
	if opts.Command == "" {
		return errors.New("container command cannot be empty")
	}
	if opts.Limits.Pids == 0 {
		opts.Limits.Pids = defaultPidsMax
	}
	return nil
}

func resolveRootfsDir(rootfsDir string) (string, error) {
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

func prepareHostFilesystem(opts Options) (func(), error) {
	// Normalize the resolver before init hides /run with a fresh tmpfs.
	if err := setupResolvConf(opts.RootfsDir); err != nil {
		fmt.Fprintf(os.Stderr, "containy: warning: setup resolver: %v\n", err)
	}

	// User bind mounts are inherited by the child's copied mount namespace.
	applied, err := applyMounts(opts.RootfsDir, opts.Mounts)
	if err != nil {
		return nil, fmt.Errorf("apply mounts: %w", err)
	}
	return func() { cleanupMounts(applied) }, nil
}

func createContainerCgroup(containerID string, limits cgroup.Limits) (*cgroup.Group, error) {
	group, err := cgroup.Create(containerID, limits)
	if err != nil {
		return nil, fmt.Errorf("create cgroup: %w", err)
	}
	return group, nil
}

// createInitCommand prepares a re-execution of the current binary as the
// container's private init process.
//
// The supervisor and init process need to exchange configuration before init
// sets up the container filesystem and calls chroot. The supervisor serializes
// that configuration as JSON into an anonymous, memory-backed file created by
// createBootstrapFile. It then passes the file through exec.Cmd.ExtraFiles,
// avoiding a persistent config file and keeping internal bootstrap data out of
// command-line arguments and environment variables.
//
// In the child process, os/exec maps ExtraFiles[0] to file descriptor 3 because
// descriptors 0, 1, and 2 are reserved for stdin, stdout, and stderr. The
// package init function recognizes initMarkerEnv and calls
// runContainerInit(bootstrapFD), which reads and closes this file descriptor.
// The descriptor number is local to the child; the underlying file may have
// had a different descriptor number in the supervisor.
func createInitCommand(opts Options, containerID string, cgroupFD int) (*exec.Cmd, *os.File, error) {
	bootstrap, err := createBootstrapFile(initConfig{
		RootfsDir: opts.RootfsDir,
		Hostname:  containerID,
		Command:   opts.Command,
		Args:      opts.Args,
	})
	if err != nil {
		return nil, nil, err
	}

	cmd := exec.Command("/proc/self/exe")
	// bootstrap is the first extra file, so os/exec installs it as bootstrapFD
	// (descriptor 3) in the child. Keep the constant and ordering in sync.
	cmd.ExtraFiles = []*os.File{bootstrap}
	cmd.Env = append(os.Environ(), initMarkerEnv)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWUTS | syscall.CLONE_NEWIPC,
		Pdeathsig:   syscall.SIGKILL,
		UseCgroupFD: true,
		CgroupFD:    cgroupFD,
	}
	return cmd, bootstrap, nil
}

func executeInitCommand(cmd *exec.Cmd, opts Options) error {
	if opts.TTY {
		return runWithTTY(cmd, opts)
	}
	cmd.SysProcAttr.Setpgid = true
	if opts.Interactive {
		cmd.Stdin = os.Stdin
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return executeCmd(cmd)
}

func finishCgroup(group *cgroup.Group) {
	if killed, err := group.OOMKilled(); err != nil {
		fmt.Fprintf(os.Stderr, "containy: warning: read cgroup OOM status: %v\n", err)
	} else if killed {
		fmt.Fprintln(os.Stderr, "containy: workload experienced a cgroup out-of-memory kill")
	}
	if err := group.Cleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "containy: warning: cleanup cgroup: %v\n", err)
	}
}

// runContainerInit consumes the supervisor's bootstrap configuration, prepares
// the container filesystem and environment, and replaces the current process
// with the requested workload. A successful unix.Exec never returns.
func runContainerInit(fd int) error {
	config, err := readInitConfig(fd)
	if err != nil {
		return err
	}
	if err := unix.Sethostname([]byte(config.Hostname)); err != nil {
		return fmt.Errorf("set hostname: %w", err)
	}

	cleanup, err := rootfs.SetupRuntimeFilesystems(config.RootfsDir)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := Chroot(config.RootfsDir); err != nil {
		return err
	}
	environment := workloadEnvironment()
	if err := installEnvironment(environment); err != nil {
		return err
	}

	const HostnameFile = "/etc/hostname"
	if err := os.WriteFile(HostnameFile, []byte(config.Hostname+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", HostnameFile, err)
	}

	command, err := resolveCommand(config.Command)
	if err != nil {
		return err
	}
	argv := append([]string{config.Command}, config.Args...)
	if err := unix.Exec(command, argv, environment); err != nil {
		return fmt.Errorf("exec %s: %w", config.Command, err)
	}
	return errors.New("exec unexpectedly returned")
}

// readInitConfig reads, decodes, and performs basic validation of the private
// init bootstrap document on fd. It takes ownership of fd and closes it after
// reading. Reading one byte beyond maxBootstrap lets the function distinguish
// a payload at the limit from an oversized payload without reading unbounded
// input into memory.
func readInitConfig(fd int) (initConfig, error) {
	file := os.NewFile(uintptr(fd), "containy-bootstrap")
	if file == nil {
		return initConfig{}, errors.New("bootstrap file descriptor is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBootstrap+1))
	if err != nil {
		file.Close()
		return initConfig{}, fmt.Errorf("read bootstrap: %w", err)
	}
	if err := file.Close(); err != nil {
		return initConfig{}, fmt.Errorf("close bootstrap: %w", err)
	}
	if len(data) > maxBootstrap {
		return initConfig{}, fmt.Errorf("bootstrap exceeds %d bytes", maxBootstrap)
	}

	var config initConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return initConfig{}, fmt.Errorf("decode bootstrap: %w", err)
	}
	if !filepath.IsAbs(config.RootfsDir) || config.Hostname == "" || config.Command == "" {
		return initConfig{}, errors.New("invalid bootstrap configuration")
	}
	return config, nil
}

func installEnvironment(environment []string) error {
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
	}
	return nil
}

func workloadEnvironment() []string {
	termEnv := os.Getenv("TERM")
	if termEnv == "" {
		termEnv = "xterm-256color"
	}
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"USER=root",
		"LOGNAME=root",
		"TERM=" + termEnv,
	}
}

func resolveCommand(command string) (string, error) {
	if strings.ContainsRune(command, '/') {
		return command, nil
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("resolve command %s: %w", command, err)
	}
	return path, nil
}

func newContainerID() (string, error) {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate container ID: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// createBootstrapFile serializes config into an anonymous in-memory file and
// rewinds it for the init process. The returned file remains owned by the
// caller; createInitCommand passes it as the child's first extra file.
func createBootstrapFile(config initConfig) (*os.File, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode bootstrap: %w", err)
	}
	if len(data) > maxBootstrap {
		return nil, fmt.Errorf("bootstrap exceeds %d bytes", maxBootstrap)
	}
	fd, err := unix.MemfdCreate("containy-bootstrap", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("create bootstrap memfd: %w", err)
	}
	file := os.NewFile(uintptr(fd), "containy-bootstrap")
	if _, err := file.Write(data); err != nil {
		file.Close()
		return nil, fmt.Errorf("write bootstrap: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, fmt.Errorf("rewind bootstrap: %w", err)
	}
	return file, nil
}

func runWithTTY(cmd *exec.Cmd, opts Options) error {
	masterFile, slaveFile, err := openPTY()
	if err != nil {
		return fmt.Errorf("openpty: %w", err)
	}
	defer masterFile.Close()
	defer slaveFile.Close()

	masterFd := int(masterFile.Fd())

	cmd.Stdin = slaveFile
	cmd.Stdout = slaveFile
	cmd.Stderr = slaveFile
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0

	stdinFd := int(os.Stdin.Fd())
	if term.IsTerminal(stdinFd) {
		oldState, err := term.MakeRaw(stdinFd)
		if err == nil {
			defer func() { _ = term.Restore(stdinFd, oldState) }()
		}

		if ws, err := unix.IoctlGetWinsize(stdinFd, unix.TIOCGWINSZ); err == nil {
			_ = unix.IoctlSetWinsize(masterFd, unix.TIOCSWINSZ, ws)
		}

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGWINCH)
		defer signal.Stop(sigChan)

		go func() {
			for range sigChan {
				if ws, err := unix.IoctlGetWinsize(stdinFd, unix.TIOCGWINSZ); err == nil {
					_ = unix.IoctlSetWinsize(masterFd, unix.TIOCSWINSZ, ws)
				}
			}
		}()
	}

	if opts.Interactive {
		go func() {
			_, _ = io.Copy(masterFile, os.Stdin)
		}()
	}

	signalChan := make(chan os.Signal, 8)
	signal.Notify(signalChan, forwardedSignals...)
	defer signal.Stop(signalChan)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec start: %w", err)
	}
	doneSignals := relaySignals(cmd.Process.Pid, signalChan)
	defer close(doneSignals)

	doneCopy := make(chan struct{})
	go func() {
		_, _ = io.Copy(os.Stdout, masterFile)
		close(doneCopy)
	}()

	waitErr := cmd.Wait()
	_ = slaveFile.Close()
	<-doneCopy

	return extractExitError(waitErr)
}

func openPTY() (master *os.File, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	var unlock int
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, master.Fd(), uintptr(unix.TIOCSPTLCK), uintptr(unsafe.Pointer(&unlock)))
	if errno != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("unlockpt (TIOCSPTLCK): %w", errno)
	}

	var ptyNum int
	_, _, errno = unix.Syscall(unix.SYS_IOCTL, master.Fd(), uintptr(unix.TIOCGPTN), uintptr(unsafe.Pointer(&ptyNum)))
	if errno != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("ptsname (TIOCGPTN): %w", errno)
	}

	slavePath := fmt.Sprintf("/dev/pts/%d", ptyNum)
	slave, err = os.OpenFile(slavePath, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("open slave %s: %w", slavePath, err)
	}
	return master, slave, nil
}

func executeCmd(cmd *exec.Cmd) error {
	signalChan := make(chan os.Signal, 8)
	signal.Notify(signalChan, forwardedSignals...)
	defer signal.Stop(signalChan)

	if err := cmd.Start(); err != nil {
		return extractExitError(err)
	}
	doneSignals := relaySignals(cmd.Process.Pid, signalChan)
	defer close(doneSignals)
	return extractExitError(cmd.Wait())
}

// relaySignals forwards supervisor signals to the workload process group. The
// workload is PID 1 and may ignore signals with default dispositions, so the
// first termination signal also starts a bounded SIGKILL escalation. Waiting
// for the process after forwarding lets normal supervisor cleanup still run.
func relaySignals(pid int, signals <-chan os.Signal) chan struct{} {
	done := make(chan struct{})
	var escalateOnce sync.Once
	go func() {
		for {
			select {
			case signal := <-signals:
				sig, ok := signal.(syscall.Signal)
				if !ok {
					continue
				}
				_ = unix.Kill(-pid, sig)
				if sig == syscall.SIGINT || sig == syscall.SIGTERM || sig == syscall.SIGHUP || sig == syscall.SIGQUIT {
					escalateOnce.Do(func() {
						go func() {
							timer := time.NewTimer(signalGrace)
							defer timer.Stop()
							select {
							case <-timer.C:
								_ = unix.Kill(-pid, syscall.SIGKILL)
							case <-done:
							}
						}()
					})
				}
			case <-done:
				return
			}
		}
	}()
	return done
}

func extractExitError(err error) error {
	if err == nil {
		return nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() {
				return fmt.Errorf("signal: %v", status.Signal())
			}
			return fmt.Errorf("exit code %d", status.ExitStatus())
		}
		return err
	}
	return fmt.Errorf("exec: %w", err)
}

// Chroot performs chroot followed by chdir to the new root.
func Chroot(rootfsDir string) error {
	if err := unix.Chroot(rootfsDir); err != nil {
		return fmt.Errorf("chroot %s: %w", rootfsDir, err)
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}
	return nil
}

// setupResolvConf normalizes /etc/resolv.conf to a regular file so mounting a
// fresh tmpfs at /run cannot hide a systemd-style resolver target.
func setupResolvConf(rootfsDir string) error {
	const resolvConf = "/etc/resolv.conf"
	const fallbackResolverConfig = "nameserver 8.8.8.8\nnameserver 1.1.1.1\n"

	etcPath := filepath.Join(rootfsDir, "etc")
	if info, err := os.Lstat(etcPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%s must be a real directory", etcPath)
		}
	} else if os.IsNotExist(err) {
		if err := os.Mkdir(etcPath, 0o755); err != nil {
			return fmt.Errorf("mkdir /etc: %w", err)
		}
	} else {
		return fmt.Errorf("inspect /etc: %w", err)
	}

	target := filepath.Join(rootfsDir, resolvConf)
	confContent, err := os.ReadFile("/etc/resolv.conf")
	if err != nil || len(confContent) == 0 {
		confContent = []byte(fallbackResolverConfig)
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(target); err != nil {
			return fmt.Errorf("remove resolver symlink: %w", err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect resolver configuration: %w", err)
	}
	if err := os.WriteFile(target, confContent, 0o644); err != nil {
		return fmt.Errorf("write resolver configuration: %w", err)
	}
	return nil
}
