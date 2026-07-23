package cgroup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteLimits(t *testing.T) {
	leaf := t.TempDir()
	for _, name := range []string{"memory.max", "memory.oom.group", "cpu.max", "pids.max"} {
		if err := os.WriteFile(filepath.Join(leaf, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	err := writeLimits(leaf, Limits{
		MemoryBytes: 128 << 20,
		CPUQuota:    50_000,
		Pids:        64,
	})
	if err != nil {
		t.Fatalf("writeLimits() failed: %v", err)
	}

	assertFileContent(t, filepath.Join(leaf, "memory.max"), "134217728\n")
	assertFileContent(t, filepath.Join(leaf, "memory.oom.group"), "1\n")
	assertFileContent(t, filepath.Join(leaf, "cpu.max"), "50000 100000\n")
	assertFileContent(t, filepath.Join(leaf, "pids.max"), "64\n")
}

func TestWriteUnlimitedLimits(t *testing.T) {
	leaf := t.TempDir()
	for _, name := range []string{"memory.max", "cpu.max", "pids.max"} {
		if err := os.WriteFile(filepath.Join(leaf, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeLimits(leaf, Limits{Pids: 256}); err != nil {
		t.Fatalf("writeLimits() failed: %v", err)
	}
	assertFileContent(t, filepath.Join(leaf, "memory.max"), "max\n")
	assertFileContent(t, filepath.Join(leaf, "cpu.max"), "max 100000\n")
	assertFileContent(t, filepath.Join(leaf, "pids.max"), "256\n")
}

func TestReadEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events")
	if err := os.WriteFile(path, []byte("low 0\noom 2\noom_kill 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	value, err := readEvent(path, "oom_kill")
	if err != nil {
		t.Fatal(err)
	}
	if value != 3 {
		t.Fatalf("oom_kill = %d, want 3", value)
	}
	if _, err := readEvent(path, "missing"); err == nil {
		t.Fatal("expected missing event error")
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Errorf("%s = %q, want %q", filepath.Base(path), data, want)
	}
}
