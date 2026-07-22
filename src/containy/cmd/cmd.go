// Package cmd implements the containy CLI dispatch.
//
// The minimal instance supports three subcommands:
//
//   - run:     extract a Docker image / docker-save tar / rootfs dir and
//     chroot into it to run a command in the foreground.
//   - import:  wrap `docker save` to produce a docker-save tar from an image.
//   - help:    print usage.
package cmd

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hxy9243/go-dojo/containy/internal/docker"
	"github.com/hxy9243/go-dojo/containy/internal/rootfs"
	"github.com/hxy9243/go-dojo/containy/internal/runtime"
)

// usage prints the containy CLI help text. The options section is rendered by
// flag.FlagSet so it always reflects the flags accepted by runCmd.
func usage(w io.Writer) {
	fmt.Fprint(w, `containy — minimal container runtime

Usage:
  containy run [options] <image|tar|rootfs-dir> <command> [args...]
  containy import <docker-image> <output-tar>
  containy help

Subcommands:
  run      Extract the image (or use a directory directly), chroot into
           the rootfs, and execute <command> in the foreground.
  import   Wrap `+"`docker save`"+` to produce a docker-save tar archive.
  help     Print this help message.

Options for run:
`)
	printFlagDefaults(newRunFlagSet(io.Discard, w, &runOptions{}), w)
	fmt.Fprint(w, `

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

type runOptions struct {
	interactive bool
	tty         bool
	mounts      []runtime.Mount
}

// volumeFlag implements flag.Value and appends each -v/--volume occurrence.
type volumeFlag struct {
	mounts *[]runtime.Mount
}

func (v volumeFlag) String() string { return "" }

func (v volumeFlag) Set(spec string) error {
	mount, err := parseVolumeFlag(spec)
	if err != nil {
		return fmt.Errorf("invalid volume specification: %w", err)
	}
	*v.mounts = append(*v.mounts, mount)
	return nil
}

// boolPairFlag lets -it and -ti remain boolean flags while using flag.FlagSet.
type boolPairFlag struct {
	first  *bool
	second *bool
}

func (f boolPairFlag) String() string { return "false" }

func (f boolPairFlag) Set(value string) error {
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	*f.first = enabled
	*f.second = enabled
	return nil
}

func (boolPairFlag) IsBoolFlag() bool { return true }

func newRunFlagSet(parseOutput, helpOutput io.Writer, options *runOptions) *flag.FlagSet {
	flags := flag.NewFlagSet("containy run", flag.ContinueOnError)
	flags.SetOutput(parseOutput)

	flags.BoolVar(&options.interactive, "i", false, "Keep STDIN open even if not attached")
	flags.BoolVar(&options.interactive, "interactive", false, "Keep STDIN open even if not attached")
	flags.BoolVar(&options.tty, "t", false, "Allocate a pseudo-TTY")
	flags.BoolVar(&options.tty, "tty", false, "Allocate a pseudo-TTY")
	flags.Var(boolPairFlag{&options.interactive, &options.tty}, "it", "Short for -i -t")
	flags.Var(boolPairFlag{&options.interactive, &options.tty}, "ti", "Short for -i -t")
	flags.Var(volumeFlag{&options.mounts}, "v", "Bind mount host path into container (`<host>:<container>[:ro|rw]`)")
	flags.Var(volumeFlag{&options.mounts}, "volume", "Bind mount host path into container (`<host>:<container>[:ro|rw]`)")

	flags.Usage = func() {
		fmt.Fprintln(helpOutput, "Usage: containy run [options] <image|tar|rootfs-dir> <command> [args...]")
		fmt.Fprintln(helpOutput, "\nOptions:")
		printFlagDefaults(flags, helpOutput)
	}

	return flags
}

func printFlagDefaults(flags *flag.FlagSet, output io.Writer) {
	originalOutput := flags.Output()
	flags.SetOutput(output)
	flags.PrintDefaults()
	flags.SetOutput(originalOutput)
}

// formatRunFlagError retains the CLI's established error messages while the
// parsing and validation itself is delegated to flag.FlagSet.
func formatRunFlagError(err error, args []string) error {
	const unknownPrefix = "flag provided but not defined: -"
	if strings.HasPrefix(err.Error(), unknownPrefix) {
		name := strings.TrimPrefix(err.Error(), unknownPrefix)
		for _, arg := range args {
			providedName := strings.SplitN(strings.TrimLeft(arg, "-"), "=", 2)[0]
			if providedName == name {
				return fmt.Errorf("unknown flag %q", arg)
			}
		}
	}

	if err.Error() == "flag needs an argument: -v" || err.Error() == "flag needs an argument: -volume" {
		return errors.New("flag -v/--volume requires an argument")
	}

	return err
}

// runCmd implements `containy run`.
func runCmd(args []string) error {
	options := runOptions{}
	flags := newRunFlagSet(os.Stderr, os.Stdout, &options)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return formatRunFlagError(err, args)
	}

	remaining := flags.Args()
	if len(remaining) < 2 {
		usage(os.Stderr)
		return errors.New("run requires <image|tar|rootfs-dir> <command> [args...]")
	}

	source := remaining[0]
	command := remaining[1]
	commandArgs := remaining[2:]

	fmt.Fprintln(os.Stderr, "containy is an educational runtime, not a secure sandbox.")
	fmt.Fprintln(os.Stderr, "Run only trusted rootfs archives and trusted commands.")

	rootfsDir, cleanup, err := prepareRootfs(source)
	if err != nil {
		return fmt.Errorf("run: prepare rootfs: %w", err)
	}
	defer cleanup()

	fmt.Fprintf(os.Stderr, "containy: rootfs ready at %s\n", rootfsDir)
	fmt.Fprintf(os.Stderr, "containy: exec %s %s\n", command, strings.Join(commandArgs, " "))

	return runtime.RunWithOptions(runtime.Options{
		RootfsDir:   rootfsDir,
		Command:     command,
		Args:        commandArgs,
		Interactive: options.interactive,
		TTY:         options.tty,
		Mounts:      options.mounts,
	})
}

func parseVolumeFlag(spec string) (runtime.Mount, error) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return runtime.Mount{}, fmt.Errorf("invalid volume format %q, expected <host>:<container>[:ro|rw]", spec)
	}

	hostPath := parts[0]
	containerPath := parts[1]

	if hostPath == "" {
		return runtime.Mount{}, fmt.Errorf("host source path cannot be empty in %q", spec)
	}

	absHost, err := filepath.Abs(hostPath)
	if err != nil {
		return runtime.Mount{}, fmt.Errorf("resolve host path %q: %w", hostPath, err)
	}

	if containerPath == "" || !strings.HasPrefix(containerPath, "/") {
		return runtime.Mount{}, fmt.Errorf("container destination %q must be an absolute path", containerPath)
	}

	cleanContainer := filepath.Clean(containerPath)
	if !filepath.IsAbs(cleanContainer) {
		return runtime.Mount{}, fmt.Errorf("invalid container path %q", containerPath)
	}

	readOnly := false
	if len(parts) == 3 {
		switch parts[2] {
		case "ro":
			readOnly = true
		case "rw":
			readOnly = false
		default:
			return runtime.Mount{}, fmt.Errorf("invalid volume mode %q in %q (must be ro or rw)", parts[2], spec)
		}
	}

	return runtime.Mount{
		Source:      absHost,
		Destination: cleanContainer,
		ReadOnly:    readOnly,
	}, nil
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
