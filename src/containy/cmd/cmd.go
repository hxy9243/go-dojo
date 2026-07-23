// Package cmd implements the containy CLI dispatch.
package cmd

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hxy9243/go-dojo/containy/internal/cgroup"
	"github.com/hxy9243/go-dojo/containy/internal/docker"
	"github.com/hxy9243/go-dojo/containy/internal/rootfs"
	"github.com/hxy9243/go-dojo/containy/internal/runtime"
)

func usage(w io.Writer) {
	fmt.Fprint(w, `containy — minimal container runtime

Usage:
  containy run [options] <image|tar|rootfs-dir> <command> [args...]
  containy import <docker-image> <output-tar>
  containy help

Subcommands:
  run      Prepare the rootfs, runtime filesystems, and cgroup, then execute
           <command> in private Linux namespaces.
  import   Wrap docker save to produce a docker-save tar archive.
  help     Print this help message.

Options for run:
`)
	printFlagDefaults(newRunFlagSet(io.Discard, w, &runOptions{}), w)
	fmt.Fprint(w, `

Run input:
  - A directory path is used directly as the rootfs.
  - A .tar file is treated as a docker-save archive and extracted.
  - An image name (e.g. ubuntu:latest) triggers docker save first.

Note:
  containy is an educational runtime, not a secure sandbox.
  Run only trusted rootfs archives and trusted commands.
`)
}

// Run dispatches a CLI invocation.
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
	memory      string
	cpu         string
	pids        uint64
}

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
	flags.Var(volumeFlag{&options.mounts}, "v", "Bind mount `<host>:<container>[:ro|rw]`")
	flags.Var(volumeFlag{&options.mounts}, "volume", "Bind mount `<host>:<container>[:ro|rw]`")
	flags.StringVar(&options.memory, "m", "", "Memory limit in bytes, or with k, m, or g suffix")
	flags.StringVar(&options.memory, "memory", "", "Memory limit in bytes, or with k, m, or g suffix")
	flags.StringVar(&options.cpu, "cpu", "", "CPU allowance in cores")
	flags.Uint64Var(&options.pids, "pids", 256, "Maximum number of processes")
	flags.Usage = func() {
		fmt.Fprintln(helpOutput, "Usage: containy run [options] <image|tar|rootfs-dir> <command> [args...]")
		fmt.Fprintln(helpOutput, "\nOptions:")
		printFlagDefaults(flags, helpOutput)
	}
	return flags
}

func printFlagDefaults(flags *flag.FlagSet, output io.Writer) {
	original := flags.Output()
	flags.SetOutput(output)
	flags.PrintDefaults()
	flags.SetOutput(original)
}

func formatRunFlagError(err error, args []string) error {
	const unknownPrefix = "flag provided but not defined: -"
	if strings.HasPrefix(err.Error(), unknownPrefix) {
		name := strings.TrimPrefix(err.Error(), unknownPrefix)
		for _, arg := range args {
			provided := strings.SplitN(strings.TrimLeft(arg, "-"), "=", 2)[0]
			if provided == name {
				return fmt.Errorf("unknown flag %q", arg)
			}
		}
	}
	if err.Error() == "flag needs an argument: -v" || err.Error() == "flag needs an argument: -volume" {
		return errors.New("flag -v/--volume requires an argument")
	}
	return err
}

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
	limits, err := parseLimits(options)
	if err != nil {
		return fmt.Errorf("invalid resource limits: %w", err)
	}

	fmt.Fprintln(os.Stderr, "containy is an educational runtime, not a secure sandbox.")
	fmt.Fprintln(os.Stderr, "Run only trusted rootfs archives and trusted commands.")

	rootfsDir, cleanup, err := prepareRootfs(remaining[0])
	if err != nil {
		return fmt.Errorf("run: prepare rootfs: %w", err)
	}
	defer cleanup()

	command := remaining[1]
	commandArgs := remaining[2:]
	fmt.Fprintf(os.Stderr, "containy: rootfs ready at %s\n", rootfsDir)
	fmt.Fprintf(os.Stderr, "containy: exec %s %s\n", command, strings.Join(commandArgs, " "))

	return runtime.RunWithOptions(runtime.Options{
		RootfsDir:   rootfsDir,
		Command:     command,
		Args:        commandArgs,
		Interactive: options.interactive,
		TTY:         options.tty,
		Mounts:      options.mounts,
		Limits:      limits,
	})
}

func parseLimits(options runOptions) (cgroup.Limits, error) {
	memoryBytes, err := parseMemoryLimit(options.memory)
	if err != nil {
		return cgroup.Limits{}, err
	}
	cpuQuota, err := parseCPUQuota(options.cpu)
	if err != nil {
		return cgroup.Limits{}, err
	}
	if options.pids == 0 || options.pids > 4_194_304 {
		return cgroup.Limits{}, errors.New("--pids must be in the range 1..4194304")
	}
	return cgroup.Limits{MemoryBytes: memoryBytes, CPUQuota: cpuQuota, Pids: options.pids}, nil
}

func parseMemoryLimit(value string) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	multiplier := uint64(1)
	number := value
	switch strings.ToLower(value[len(value)-1:]) {
	case "k":
		multiplier = 1 << 10
		number = value[:len(value)-1]
	case "m":
		multiplier = 1 << 20
		number = value[:len(value)-1]
	case "g":
		multiplier = 1 << 30
		number = value[:len(value)-1]
	}
	parsed, err := strconv.ParseUint(number, 10, 64)
	if err != nil || parsed == 0 {
		return 0, fmt.Errorf("memory limit %q must be a positive integer with optional k, m, or g suffix", value)
	}
	if parsed > math.MaxUint64/multiplier {
		return 0, fmt.Errorf("memory limit %q overflows uint64", value)
	}
	return parsed * multiplier, nil
}

func parseCPUQuota(value string) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	cores, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(cores) || math.IsInf(cores, 0) || cores <= 0 {
		return 0, fmt.Errorf("CPU allowance %q must be a positive finite number", value)
	}
	quota := math.Ceil(cores * 100_000)
	if quota > math.MaxUint64 {
		return 0, fmt.Errorf("CPU allowance %q is too large", value)
	}
	if quota < 1_000 {
		quota = 1_000
	}
	return uint64(quota), nil
}

func parseVolumeFlag(spec string) (runtime.Mount, error) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return runtime.Mount{}, fmt.Errorf("invalid volume format %q, expected <host>:<container>[:ro|rw]", spec)
	}
	if parts[0] == "" {
		return runtime.Mount{}, fmt.Errorf("host source path cannot be empty in %q", spec)
	}
	source, err := filepath.Abs(parts[0])
	if err != nil {
		return runtime.Mount{}, fmt.Errorf("resolve host path %q: %w", parts[0], err)
	}
	destination := filepath.Clean(parts[1])
	if parts[1] == "" || !filepath.IsAbs(destination) {
		return runtime.Mount{}, fmt.Errorf("container destination %q must be an absolute path", parts[1])
	}

	readOnly := false
	if len(parts) == 3 {
		switch parts[2] {
		case "ro":
			readOnly = true
		case "rw":
		default:
			return runtime.Mount{}, fmt.Errorf("invalid volume mode %q in %q (must be ro or rw)", parts[2], spec)
		}
	}
	return runtime.Mount{Source: source, Destination: destination, ReadOnly: readOnly}, nil
}

func importCmd(args []string) error {
	if len(args) != 2 {
		usage(os.Stderr)
		return errors.New("import requires <docker-image> <output-tar>")
	}
	output, err := filepath.Abs(args[1])
	if err != nil {
		return fmt.Errorf("import: resolve output path: %w", err)
	}
	fmt.Fprintf(os.Stderr, "containy: docker save %s → %s\n", args[0], output)
	return docker.Save(args[0], output)
}

func prepareRootfs(source string) (rootfsDir string, cleanup func(), err error) {
	return prepareRootfsWithSave(source, docker.Save)
}

func prepareRootfsWithSave(source string, saveImage func(string, string) error) (rootfsDir string, cleanup func(), err error) {
	info, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) && !looksLikeRootfsPath(source) {
			return importImageToTemp(source, saveImage)
		}
		return "", nil, fmt.Errorf("stat %s: %w", source, err)
	}
	if info.IsDir() {
		absolute, err := filepath.Abs(source)
		if err != nil {
			return "", nil, fmt.Errorf("resolve %s: %w", source, err)
		}
		return absolute, func() {}, nil
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular file or directory", source)
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %s: %w", source, err)
	}
	return extractToTemp(absolute)
}

func looksLikeRootfsPath(source string) bool {
	lower := strings.ToLower(source)
	return filepath.IsAbs(source) ||
		strings.HasPrefix(source, ".") ||
		strings.HasSuffix(lower, ".tar") ||
		strings.HasSuffix(lower, ".tar.gz") ||
		strings.HasSuffix(lower, ".tgz")
}

func importImageToTemp(image string, saveImage func(string, string) error) (string, func(), error) {
	archive, err := os.CreateTemp("", "containy-import-*.tar")
	if err != nil {
		return "", nil, fmt.Errorf("create temporary image archive: %w", err)
	}
	archivePath := archive.Name()
	if err := archive.Close(); err != nil {
		removeErr := os.Remove(archivePath)
		return "", nil, errors.Join(
			fmt.Errorf("close temporary image archive: %w", err),
			wrapRemoveError(archivePath, removeErr),
		)
	}
	if err := saveImage(image, archivePath); err != nil {
		removeErr := os.Remove(archivePath)
		return "", nil, errors.Join(
			fmt.Errorf("docker save: %w", err),
			wrapRemoveError(archivePath, removeErr),
		)
	}
	rootfsDir, cleanup, err := extractToTemp(archivePath)
	if removeErr := os.Remove(archivePath); removeErr != nil {
		if cleanup != nil {
			cleanup()
		}
		return "", nil, errors.Join(err, fmt.Errorf("remove temporary image archive %q: %w", archivePath, removeErr))
	}
	return rootfsDir, cleanup, err
}

func wrapRemoveError(path string, err error) error {
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return fmt.Errorf("remove temporary image archive %q: %w", path, err)
}

func extractToTemp(tarPath string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "containy-rootfs-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp rootfs dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	if err := rootfs.Extract(tarPath, tmpDir); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("extract %s: %w", tarPath, err)
	}
	return tmpDir, cleanup, nil
}
