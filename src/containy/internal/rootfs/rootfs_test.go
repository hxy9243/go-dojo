package rootfs

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// makeDockerSaveTar creates a docker-save format tar in memory.
// It contains a manifest.json and one or more layer tars.
func makeDockerSaveTar(t *testing.T, layers map[string][]byte, manifest dockerManifest) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Write manifest.json.
	manifestJSON, _ := json.Marshal(manifest)
	if err := tw.WriteHeader(&tar.Header{
		Name: "manifest.json",
		Mode: 0o644,
		Size: int64(len(manifestJSON)),
	}); err != nil {
		t.Fatalf("write manifest header: %v", err)
	}
	tw.Write(manifestJSON)

	// Write each layer tar.
	for name, data := range layers {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(data)),
		}); err != nil {
			t.Fatalf("write layer header: %v", err)
		}
		tw.Write(data)
	}

	tw.Close()
	return buf.Bytes()
}

// makeLayerTar creates a layer tar in memory from a list of entries.
type layerEntry struct {
	Name     string
	Content  string
	Type     byte
	Mode     int64
	Linkname string
}

func makeLayerTar(t *testing.T, entries []layerEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for _, e := range entries {
		typ := e.Type
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.Mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Name:     e.Name,
			Mode:     mode,
			Size:     int64(len(e.Content)),
			Typeflag: typ,
			Linkname: e.Linkname,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", e.Name, err)
		}
		if e.Content != "" {
			tw.Write([]byte(e.Content))
		}
	}

	tw.Close()
	return buf.Bytes()
}

func TestExtract_SingleLayer(t *testing.T) {
	layerData := makeLayerTar(t, []layerEntry{
		{Name: "bin/", Type: tar.TypeDir, Mode: 0o755},
		{Name: "bin/hello", Content: "#!/bin/sh\necho hello\n", Mode: 0o755},
		{Name: "etc/passwd", Content: "root:x:0:0:root:/root:/bin/sh\n"},
	})

	manifest := dockerManifest{
		{
			Config:   "config.json",
			RepoTags: []string{"test:latest"},
			Layers:   []string{"abc123/layer.tar"},
		},
	}

	tarData := makeDockerSaveTar(t, map[string][]byte{
		"abc123/layer.tar": layerData,
	}, manifest)

	// Write the docker-save tar to a temp file.
	tmpFile, err := os.CreateTemp("", "containy-test-*.tar")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(tarData)
	tmpFile.Close()

	// Extract to a temp dir.
	destDir := t.TempDir()
	if err := Extract(tmpFile.Name(), destDir); err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	// Verify files exist.
	helloPath := filepath.Join(destDir, "bin", "hello")
	data, err := os.ReadFile(helloPath)
	if err != nil {
		t.Fatalf("read bin/hello: %v", err)
	}
	if string(data) != "#!/bin/sh\necho hello\n" {
		t.Errorf("bin/hello content = %q, want hello script", string(data))
	}

	passwdPath := filepath.Join(destDir, "etc", "passwd")
	data, err = os.ReadFile(passwdPath)
	if err != nil {
		t.Fatalf("read etc/passwd: %v", err)
	}
	if string(data) != "root:x:0:0:root:/root:/bin/sh\n" {
		t.Errorf("etc/passwd content = %q", string(data))
	}

	// Verify permissions.
	info, err := os.Stat(helloPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("bin/hello should be executable, mode=%v", info.Mode())
	}
}

func TestExtract_MultipleLayers(t *testing.T) {
	// Layer 1: base files.
	layer1 := makeLayerTar(t, []layerEntry{
		{Name: "etc/", Type: tar.TypeDir, Mode: 0o755},
		{Name: "etc/config", Content: "base\n"},
		{Name: "bin/", Type: tar.TypeDir, Mode: 0o755},
		{Name: "bin/app", Content: "v1\n", Mode: 0o755},
	})

	// Layer 2: overrides etc/config, adds new file.
	layer2 := makeLayerTar(t, []layerEntry{
		{Name: "etc/config", Content: "updated\n"},
		{Name: "etc/newfile", Content: "new\n"},
	})

	manifest := dockerManifest{
		{
			Layers: []string{"layer1/layer.tar", "layer2/layer.tar"},
		},
	}

	tarData := makeDockerSaveTar(t, map[string][]byte{
		"layer1/layer.tar": layer1,
		"layer2/layer.tar": layer2,
	}, manifest)

	tmpFile, err := os.CreateTemp("", "containy-test-*.tar")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(tarData)
	tmpFile.Close()

	destDir := t.TempDir()
	if err := Extract(tmpFile.Name(), destDir); err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	// etc/config should be from layer 2.
	data, err := os.ReadFile(filepath.Join(destDir, "etc", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "updated\n" {
		t.Errorf("etc/config = %q, want 'updated'", string(data))
	}

	// etc/newfile should exist from layer 2.
	data, err = os.ReadFile(filepath.Join(destDir, "etc", "newfile"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new\n" {
		t.Errorf("etc/newfile = %q, want 'new'", string(data))
	}

	// bin/app should still exist from layer 1.
	data, err = os.ReadFile(filepath.Join(destDir, "bin", "app"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v1\n" {
		t.Errorf("bin/app = %q, want 'v1'", string(data))
	}
}

func TestExtract_WhiteoutNamed(t *testing.T) {
	// Layer 1: has a file.
	layer1 := makeLayerTar(t, []layerEntry{
		{Name: "etc/", Type: tar.TypeDir, Mode: 0o755},
		{Name: "etc/removed", Content: "should be deleted\n"},
		{Name: "etc/kept", Content: "should stay\n"},
	})

	// Layer 2: whiteout for etc/removed.
	layer2 := makeLayerTar(t, []layerEntry{
		{Name: "etc/.wh.removed"},
	})

	manifest := dockerManifest{
		{Layers: []string{"l1/layer.tar", "l2/layer.tar"}},
	}

	tarData := makeDockerSaveTar(t, map[string][]byte{
		"l1/layer.tar": layer1,
		"l2/layer.tar": layer2,
	}, manifest)

	tmpFile, _ := os.CreateTemp("", "containy-test-*.tar")
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(tarData)
	tmpFile.Close()

	destDir := t.TempDir()
	if err := Extract(tmpFile.Name(), destDir); err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	// etc/removed should not exist.
	if _, err := os.Stat(filepath.Join(destDir, "etc", "removed")); !os.IsNotExist(err) {
		t.Errorf("etc/removed should have been whiteout-deleted")
	}

	// etc/kept should still exist.
	data, err := os.ReadFile(filepath.Join(destDir, "etc", "kept"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "should stay\n" {
		t.Errorf("etc/kept = %q", string(data))
	}
}

func TestExtract_WhiteoutOpaque(t *testing.T) {
	// Layer 1: has multiple files in a dir.
	layer1 := makeLayerTar(t, []layerEntry{
		{Name: "data/", Type: tar.TypeDir, Mode: 0o755},
		{Name: "data/a", Content: "a\n"},
		{Name: "data/b", Content: "b\n"},
		{Name: "data/c", Content: "c\n"},
	})

	// Layer 2: opaque whiteout for data/, then adds new file.
	layer2 := makeLayerTar(t, []layerEntry{
		{Name: "data/.wh..wh..opq"},
		{Name: "data/new", Content: "new\n"},
	})

	manifest := dockerManifest{
		{Layers: []string{"l1/layer.tar", "l2/layer.tar"}},
	}

	tarData := makeDockerSaveTar(t, map[string][]byte{
		"l1/layer.tar": layer1,
		"l2/layer.tar": layer2,
	}, manifest)

	tmpFile, _ := os.CreateTemp("", "containy-test-*.tar")
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(tarData)
	tmpFile.Close()

	destDir := t.TempDir()
	if err := Extract(tmpFile.Name(), destDir); err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	// data/a, data/b, data/c should not exist.
	for _, name := range []string{"a", "b", "c"} {
		if _, err := os.Stat(filepath.Join(destDir, "data", name)); !os.IsNotExist(err) {
			t.Errorf("data/%s should have been opaque-whiteout-deleted", name)
		}
	}

	// data/new should exist.
	data, err := os.ReadFile(filepath.Join(destDir, "data", "new"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new\n" {
		t.Errorf("data/new = %q", string(data))
	}
}

func TestExtract_Symlink(t *testing.T) {
	layerData := makeLayerTar(t, []layerEntry{
		{Name: "usr/", Type: tar.TypeDir, Mode: 0o755},
		{Name: "usr/bin/", Type: tar.TypeDir, Mode: 0o755},
		{Name: "bin", Type: tar.TypeSymlink, Linkname: "usr/bin"},
	})

	manifest := dockerManifest{
		{Layers: []string{"l/layer.tar"}},
	}

	tarData := makeDockerSaveTar(t, map[string][]byte{
		"l/layer.tar": layerData,
	}, manifest)

	tmpFile, _ := os.CreateTemp("", "containy-test-*.tar")
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(tarData)
	tmpFile.Close()

	destDir := t.TempDir()
	if err := Extract(tmpFile.Name(), destDir); err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	linkPath := filepath.Join(destDir, "bin")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat bin: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("bin should be a symlink, mode=%v", info.Mode())
	}

	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink bin: %v", err)
	}
	if target != "usr/bin" {
		t.Errorf("bin symlink target = %q, want 'usr/bin'", target)
	}
}

func TestExtract_NoManifest(t *testing.T) {
	// Create a tar with no manifest.json.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{
		Name: "somefile",
		Mode: 0o644,
		Size: 5,
	})
	tw.Write([]byte("hello"))
	tw.Close()

	tmpFile, _ := os.CreateTemp("", "containy-test-*.tar")
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(buf.Bytes())
	tmpFile.Close()

	destDir := t.TempDir()
	err := Extract(tmpFile.Name(), destDir)
	if err == nil {
		t.Fatal("Extract should fail with no manifest")
	}
}

func TestExtract_EmptyManifest(t *testing.T) {
	// Create a tar with an empty manifest array.
	manifestJSON := []byte("[]")

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{
		Name: "manifest.json",
		Mode: 0o644,
		Size: int64(len(manifestJSON)),
	})
	tw.Write(manifestJSON)
	tw.Close()

	tmpFile, _ := os.CreateTemp("", "containy-test-*.tar")
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(buf.Bytes())
	tmpFile.Close()

	destDir := t.TempDir()
	err := Extract(tmpFile.Name(), destDir)
	if err == nil {
		t.Fatal("Extract should fail with empty manifest")
	}
}

func TestExtract_MissingLayer(t *testing.T) {
	manifest := dockerManifest{
		{Layers: []string{"nonexistent/layer.tar"}},
	}

	manifestJSON, _ := json.Marshal(manifest)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{
		Name: "manifest.json",
		Mode: 0o644,
		Size: int64(len(manifestJSON)),
	})
	tw.Write(manifestJSON)
	tw.Close()

	tmpFile, _ := os.CreateTemp("", "containy-test-*.tar")
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(buf.Bytes())
	tmpFile.Close()

	destDir := t.TempDir()
	err := Extract(tmpFile.Name(), destDir)
	if err == nil {
		t.Fatal("Extract should fail when layer is missing")
	}
}

func TestByteReader(t *testing.T) {
	data := []byte("hello world")
	r := newByteReader(data)

	buf := make([]byte, 5)
	n, err := r.Read(buf)
	if err != nil || n != 5 {
		t.Fatalf("read 5 bytes: n=%d err=%v", n, err)
	}
	if string(buf) != "hello" {
		t.Errorf("first 5 bytes = %q, want 'hello'", string(buf))
	}

	// Read remaining.
	remaining := make([]byte, 20)
	n, err = r.Read(remaining)
	if err != nil {
		t.Fatalf("read remaining: n=%d err=%v", n, err)
	}
	if string(remaining[:n]) != " world" {
		t.Errorf("remaining = %q, want ' world'", string(remaining[:n]))
	}

	// Read past end.
	n, err = r.Read(buf)
	if err == nil {
		t.Errorf("expected EOF, got n=%d", n)
	}
}