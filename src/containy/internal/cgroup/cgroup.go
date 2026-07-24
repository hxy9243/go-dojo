// Package cgroup manages containy's host-side cgroups v2 hierarchy.
package cgroup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	defaultRoot = "/sys/fs/cgroup"
	parentName  = "containy"
)

// Limits contains the cgroups v2 values applied to one container.
// Zero MemoryBytes and CPUQuota mean unlimited.
type Limits struct {
	MemoryBytes uint64
	CPUQuota    uint64
	Pids        uint64
}

// Group is a cgroup leaf owned by one running container.
type Group struct {
	path       string
	fd         *os.File
	initialOOM uint64
}

// Create prepares a cgroups v2 leaf and opens it for atomic clone placement.
func Create(containerID string, limits Limits) (*Group, error) {
	return createAt(defaultRoot, containerID, limits, true)
}

func createAt(root, containerID string, limits Limits, verifyFilesystem bool) (*Group, error) {
	if err := validateCreateInput(containerID, limits); err != nil {
		return nil, err
	}
	if verifyFilesystem {
		if err := verifyCgroup2(root); err != nil {
			return nil, err
		}
	}
	parent, err := prepareParent(root)
	if err != nil {
		return nil, err
	}

	leaf := filepath.Join(parent, containerID)
	if err := os.Mkdir(leaf, 0o700); err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("cgroup %q already exists", containerID)
		}
		return nil, fmt.Errorf("create cgroup leaf: %w", err)
	}

	removeLeaf := true
	defer func() {
		if removeLeaf {
			_ = os.Remove(leaf)
		}
	}()

	if err := writeLimits(leaf, limits); err != nil {
		return nil, err
	}
	group, err := openGroup(leaf)
	if err != nil {
		return nil, err
	}
	removeLeaf = false
	return group, nil
}

func validateCreateInput(containerID string, limits Limits) error {
	if containerID == "" || strings.ContainsAny(containerID, `/\`) || containerID == "." || containerID == ".." {
		return fmt.Errorf("invalid container ID %q", containerID)
	}
	if limits.Pids == 0 {
		return errors.New("pids limit must be greater than zero")
	}
	return nil
}

func verifyCgroup2(root string) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(root, &stat); err != nil {
		return fmt.Errorf("stat cgroup root: %w", err)
	}
	if uint64(stat.Type) != uint64(unix.CGROUP2_SUPER_MAGIC) {
		return fmt.Errorf("%s is not a cgroups v2 filesystem", root)
	}
	return nil
}

func prepareParent(root string) (string, error) {
	parent := filepath.Join(root, parentName)
	if err := os.Mkdir(parent, 0o700); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("create cgroup parent: %w", err)
	}
	if err := requireEmpty(filepath.Join(parent, "cgroup.procs")); err != nil {
		return "", fmt.Errorf("cgroup parent must contain no processes: %w", err)
	}

	controllers, err := readWords(filepath.Join(parent, "cgroup.controllers"))
	if err != nil {
		return "", fmt.Errorf("read available controllers: %w", err)
	}
	for _, required := range []string{"cpu", "memory", "pids"} {
		if !controllers[required] {
			return "", fmt.Errorf("required cgroup controller %q is unavailable", required)
		}
	}
	if err := os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"), []byte("+cpu +memory +pids\n"), 0o644); err != nil {
		return "", fmt.Errorf("enable cgroup controllers: %w", err)
	}
	return parent, nil
}

func openGroup(leaf string) (*Group, error) {
	initialOOM, err := readEvent(filepath.Join(leaf, "memory.events"), "oom_kill")
	if err != nil {
		return nil, fmt.Errorf("read initial memory.events: %w", err)
	}
	fd, err := os.Open(leaf)
	if err != nil {
		return nil, fmt.Errorf("open cgroup leaf: %w", err)
	}
	return &Group{path: leaf, fd: fd, initialOOM: initialOOM}, nil
}

// FD returns an open directory descriptor suitable for SysProcAttr.CgroupFD.
func (g *Group) FD() int {
	return int(g.fd.Fd())
}

// Path returns the host path of this cgroup leaf. It is used while the
// container init still has access to the host mount namespace to bind the
// leaf into the container's private mount namespace.
func (g *Group) Path() string {
	return g.path
}

// OOMKilled reports whether the cgroup observed an OOM kill after creation.
func (g *Group) OOMKilled() (bool, error) {
	current, err := readEvent(filepath.Join(g.path, "memory.events"), "oom_kill")
	if err != nil {
		return false, err
	}
	return current > g.initialOOM, nil
}

// Cleanup empties and removes the container leaf. It never removes the shared
// containy parent.
func (g *Group) Cleanup() error {
	if g == nil {
		return nil
	}
	var result error
	if g.fd != nil {
		result = errors.Join(result, g.fd.Close())
		g.fd = nil
	}

	populated, err := readEvent(filepath.Join(g.path, "cgroup.events"), "populated")
	if err != nil && !os.IsNotExist(err) {
		result = errors.Join(result, fmt.Errorf("read cgroup.events: %w", err))
	}
	if populated != 0 {
		killPath := filepath.Join(g.path, "cgroup.kill")
		if err := os.WriteFile(killPath, []byte("1\n"), 0o644); err != nil && !os.IsNotExist(err) {
			result = errors.Join(result, fmt.Errorf("kill remaining cgroup processes: %w", err))
		}
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
			populated, err = readEvent(filepath.Join(g.path, "cgroup.events"), "populated")
			if err == nil && populated == 0 {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	if err := os.Remove(g.path); err != nil && !os.IsNotExist(err) {
		result = errors.Join(result, fmt.Errorf("remove cgroup leaf: %w", err))
	}
	return result
}

func writeLimits(leaf string, limits Limits) error {
	memoryMax := "max\n"
	if limits.MemoryBytes != 0 {
		memoryMax = strconv.FormatUint(limits.MemoryBytes, 10) + "\n"
	}
	if err := writeControl(leaf, "memory.max", memoryMax); err != nil {
		return err
	}
	if limits.MemoryBytes != 0 {
		oomGroup := filepath.Join(leaf, "memory.oom.group")
		if _, err := os.Stat(oomGroup); err == nil {
			if err := os.WriteFile(oomGroup, []byte("1\n"), 0o644); err != nil {
				return fmt.Errorf("write memory.oom.group: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect memory.oom.group: %w", err)
		}
	}

	cpuMax := "max 100000\n"
	if limits.CPUQuota != 0 {
		cpuMax = fmt.Sprintf("%d 100000\n", limits.CPUQuota)
	}
	if err := writeControl(leaf, "cpu.max", cpuMax); err != nil {
		return err
	}
	return writeControl(leaf, "pids.max", strconv.FormatUint(limits.Pids, 10)+"\n")
}

func writeControl(leaf, name, value string) error {
	if err := os.WriteFile(filepath.Join(leaf, name), []byte(value), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func requireEmpty(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != "" {
		return errors.New("cgroup.procs is not empty")
	}
	return nil
}

func readWords(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	words := make(map[string]bool)
	for _, word := range strings.Fields(string(data)) {
		words[word] = true
	}
	return words, nil
}

func readEvent(path, key string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != key {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse %s in %s: %w", key, path, err)
		}
		return value, nil
	}
	return 0, fmt.Errorf("%s not found in %s", key, path)
}
