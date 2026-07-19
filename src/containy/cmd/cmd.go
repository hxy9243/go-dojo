// Package cmd implements the containy CLI dispatch.
//
// The minimal instance supports three subcommands:
//
//   - run:     extract a Docker image / docker-save tar / rootfs dir and
//              chroot into it to run a command in the foreground.
//   - import:  wrap `docker save` to produce a docker-save tar from an image.
//   - help:    print usage.
package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hxy9243/go-dojo/containy/internal/docker"
	"github.com/hxy9243/go-dojo/containy/internal/rootfs"
	"github.com/hxy9243/go-dojo/containy/internal/runtime"
)

// usage prints the containy CLI help text.
func usage(w *os.File) {
	fmt.Fprint(w, `containy — minimal container runtime

Usage:
  containy run <image|tar|rootfs-dir> <command> [args...]
  containy import <docker-image> <output-tar>
  containy help

Subcommands:
  run      Extract the image (or use a directory directly), chroot into
           the rootfs, and execute <command> in the foreground.
  import   Wrap `+"`docker save`"+` to produce a docker-save tar archive.
  help     Print this help message.

Run input:
  - A directory path is used directly as the rootfs.
  - A .tar file is treated as a docker-save archive and extracted.
  - An image name (e.g. ubuntu:latest) triggers `+"`docker save`"+` first.

Note:
  containy is an educational runtime, not a secure sandbox.
  Run only trusted rootfs archives and trusted commands.
`)
}

// Run is the main CLI entry point. It dispatches to subcommands based on
// the first argument.
func Run(args []string) error {
	if len(args) == 0 {
		usage(os.Stderr)
		return errors.New("no subcommand provided")
	}

	switch args[0] {
	case "run":
		return runCmd(args[1:])
	case "import":
		return importCmd(args[1:])
	case "help", "--help", "-h":
		usage(os.Stdout)
		return nil
	default:
		usage(os.Stderr)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// runCmd implements `containy run`.
func runCmd(args []string) error {
	if len(args) < 2 {
		usage(os.Stderr)
		return errors.New("run requires <image|tar|rootfs-dir> <command> [args...]")
	}

	source := args[0]
	command := args[1]
	commandArgs := args[2:]

	fmt.Fprintln(os.Stderr, "containy is an educational runtime, not a secure sandbox.")
	fmt.Fprintln(os.Stderr, "Run only trusted rootfs archives and trusted commands.")

	rootfsDir, cleanup, err := prepareRootfs(source)
	if err != nil {
		return fmt.Errorf("run: prepare rootfs: %w", err)
	}
	defer cleanup()

	fmt.Fprintf(os.Stderr, "containy: rootfs ready at %s\n", rootfsDir)
	fmt.Fprintf(os.Stderr, "containy: exec %s %s\n", command, strings.Join(commandArgs, " "))

	return runtime.Run(rootfsDir, command, commandArgs...)
}

// importCmd implements `containy import`.
func importCmd(args []string) error {
	if len(args) != 2 {
		usage(os.Stderr)
		return errors.New("import requires <docker-image> <output-tar>")
	}

	imageRef := args[0]
	outputTar := args[1]

	absOutput, err := filepath.Abs(outputTar)
	if err != nil {
		return fmt.Errorf("import: resolve output path: %w", err)
	}

	fmt.Fprintf(os.Stderr, "containy: docker save %s → %s\n", imageRef, absOutput)
	return docker.Save(imageRef, absOutput)
}

// prepareRootfs resolves the source argument to a rootfs directory.
// It returns the directory path and a cleanup function that removes any
// temporary directory created during preparation.
func prepareRootfs(source string) (rootfsDir string, cleanup func(), err error) {
	info, err := os.Stat(source)
	if err != nil {
		return "", nil, fmt.Errorf("stat %s: %w", source, err)
	}

	// Case 1: directory — use directly.
	if info.IsDir() {
		abs, err := filepath.Abs(source)
		if err != nil {
			return "", nil, fmt.Errorf("resolve %s: %w", source, err)
		}
		return abs, func() {}, nil
	}

	// Case 2: tar file — extract.
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular file or directory", source)
	}

	absSource, err := filepath.Abs(source)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %s: %w", source, err)
	}

	// Check if it looks like a docker image name (no path separator, no .tar).
	if !strings.Contains(filepath.Base(source), ".tar") && !filepath.IsAbs(source) && !strings.Contains(source, "/") {
		// Treat as docker image name — docker save first.
		tmpTar, err := os.CreateTemp("", "containy-import-*.tar")
		if err != nil {
			return "", nil, fmt.Errorf("create temp tar: %w", err)
		}
		tmpTar.Close()
		tmpTarPath := tmpTar.Name()

		if err := docker.Save(source, tmpTarPath); err != nil {
			os.Remove(tmpTarPath)
			return "", nil, fmt.Errorf("docker save: %w", err)
		}

		dir, cleanup2, err := extractToTemp(tmpTarPath)
		os.Remove(tmpTarPath)
		return dir, cleanup2, err
	}

	// It's a tar file on disk.
	return extractToTemp(absSource)
}

// extractToTemp extracts a docker-save tar into a temporary directory.
// It returns the directory path and a cleanup function.
func extractToTemp(tarPath string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "containy-rootfs-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp rootfs dir: %w", err)
	}

	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	if err := rootfs.Extract(tarPath, tmpDir); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("extract %s: %w", tarPath, err)
	}

	return tmpDir, cleanup, nil
}