//go:build integration

package rootfs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExtract_RealUbuntuTar tests extraction against the real docker-save
// ubuntu.tar in the test fixtures. This test is behind the "integration"
// build tag because it requires the large fixture file.
//
// Run with: go test -tags=integration ./internal/rootfs/
func TestExtract_RealUbuntuTar(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("extracting an image with foreign numeric ownership requires root privileges")
	}

	tarPath := "../../ubuntu/ubuntu.tar"
	if _, err := os.Stat(tarPath); err != nil {
		t.Skipf("fixture %s not found: %v", tarPath, err)
	}

	destDir := t.TempDir()
	if err := Extract(tarPath, destDir); err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	// Verify key files exist in the extracted rootfs.
	expected := []string{
		"etc/passwd",
		"bin/bash",
		"bin/sh",
		"usr/bin/env",
		"var",
		"etc",
	}

	for _, p := range expected {
		full := filepath.Join(destDir, p)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	// Verify etc/passwd has content.
	passwdData, err := os.ReadFile(filepath.Join(destDir, "etc", "passwd"))
	if err != nil {
		t.Fatalf("read etc/passwd: %v", err)
	}
	if len(passwdData) == 0 {
		t.Error("etc/passwd is empty")
	}
	t.Logf("etc/passwd: %d bytes", len(passwdData))

	// Count top-level entries.
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rootfs has %d top-level entries", len(entries))
}
