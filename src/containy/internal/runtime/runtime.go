// Package runtime implements container execution using Linux namespaces:
// unshares mount, PID, UTS, and IPC namespaces, chroots into a rootfs directory,
// and execs a command with configured stdio/TTY.
//
// This package is Linux-only and requires root privileges.
package runtime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Options configures container execution.
type Options struct {
	RootfsDir   string
	Command     string
	Args        []string
	Interactive bool // -i: keep stdin open
	TTY         bool // -t: allocate a pseudo-TTY
}

// Run chroots into rootfsDir and executes the given command with args.
// Stdio (stdin, stdout, stderr) is inherited by the child process.
// The function returns the child's exit code as an error (nil for exit 0).
func Run(rootfsDir string, command string, args ...string) error {
	return RunWithOptions(Options{
		RootfsDir:   rootfsDir,
		Command:     command,
		Args:        args,
		Interactive: true,
		TTY:         false,
	})
}

// RunWithOptions chroots into rootfsDir and executes the command with the specified Options.
func RunWithOptions(opts Options) error {
	if os.Getuid() != 0 {
		return errors.New("containy requires root privileges (run with sudo)")
	}

	cmd := exec.Command(opts.Command, opts.Args...)

	// Set up namespaces and chroot via SysProcAttr.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Chroot:     opts.RootfsDir,
		Cloneflags: syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWUTS | syscall.CLONE_NEWIPC,
	}
	cmd.Dir = "/"

	termEnv := os.Getenv("TERM")
	if termEnv == "" {
		termEnv = "xterm-256color"
	}

	// Set a minimal environment.
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"USER=root",
		"LOGNAME=root",
		"TERM=" + termEnv,
	}

	// Provide resolver configuration at the target used by OCI image resolv.conf symlinks.
	if err := setupResolvConf(opts.RootfsDir); err != nil {
		fmt.Fprintf(os.Stderr, "containy: warning: setup /run: %v\n", err)
	}

	if opts.TTY {
		return runWithTTY(cmd, opts)
	}

	if opts.Interactive {
		cmd.Stdin = os.Stdin
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return executeCmd(cmd)
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

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec start: %w", err)
	}

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

	// Unlock slave PTY (unlockpt)
	var unlock int = 0
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, master.Fd(), uintptr(unix.TIOCSPTLCK), uintptr(unsafe.Pointer(&unlock)))
	if errno != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("unlockpt (TIOCSPTLCK): %w", errno)
	}

	// Get slave PTY number (ptsname)
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
	err := cmd.Run()
	return extractExitError(err)
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

// Chroot is a low-level helper that performs chroot + chdir. It is exported
// for testing but not used directly by the CLI.
func Chroot(rootfsDir string) error {
	if err := unix.Chroot(rootfsDir); err != nil {
		return fmt.Errorf("chroot %s: %w", rootfsDir, err)
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}
	return nil
}

// Setup /etc/resolv.conf inside the container
func setupResolvConf(rootfsDir string) error {
	const resolvConf = "/etc/resolv.conf"
	const fallbackResolverConfig = "nameserver 8.8.8.8\nnameserver 1.1.1.1\n"

	stubResolverTarget := filepath.Join(rootfsDir, resolvConf)
	if err := os.MkdirAll(filepath.Dir(stubResolverTarget), 0755); err != nil {
		return fmt.Errorf("mkdir /run/systemd/resolve: %w", err)
	}

	confContent, err := os.ReadFile("/run/systemd/resolve/resolv.conf")
	if err != nil || len(confContent) == 0 {
		confContent = []byte(fallbackResolverConfig)
	}
	if err := os.WriteFile(stubResolverTarget, confContent, 0644); err != nil {
		return fmt.Errorf("write stub resolver configuration: %w", err)
	}

	return nil
}
