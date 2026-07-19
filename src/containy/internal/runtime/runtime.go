// Package runtime implements container execution using user and mount namespaces:
// unshares user and mount namespaces, chroots into a rootfs directory, and
// execs a command with inherited stdio.
//
// This package is Linux-only.
package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// Run chroots into rootfsDir and executes the given command with args.
// Stdio (stdin, stdout, stderr) is inherited by the child process.
// The function returns the child's exit code as an error (nil for exit 0).
//
// Run unshares User and Mount namespaces (CLONE_NEWUSER | CLONE_NEWNS)
// mapping the current user to root in the new namespace, allowing rootless chroot(2).
func Run(rootfsDir string, command string, args ...string) error {
	cmd := exec.Command(command, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Set up namespaces and chroot via SysProcAttr.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Chroot:     rootfsDir,
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{
			{
				ContainerID: 0,
				HostID:      os.Getuid(),
				Size:        1,
			},
		},
		GidMappings: []syscall.SysProcIDMap{
			{
				ContainerID: 0,
				HostID:      os.Getgid(),
				Size:        1,
			},
		},
		GidMappingsEnableSetgroups: false,
	}
	cmd.Dir = "/"

	// Set a minimal environment.
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"USER=root",
		"LOGNAME=root",
	}

	if err := cmd.Run(); err != nil {
		// Try to extract the exit code.
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

	return nil
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