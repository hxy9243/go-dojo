// Package rootfs extracts a docker-save tar archive into a flat rootfs
// directory.
//
// A docker-save tar contains a manifest.json that lists layer tar paths in
// order (base layer first). Each layer tar contains filesystem entries with
// paths relative to `/`. This package reads the manifest, extracts each layer
// in order, and applies OCI/Docker whiteout entries (`.wh.<name>` and
// `.wh..wh..opq`).
//
// The minimal instance trusts the docker image (per spec §2). Full path
// traversal protection will be added in the complete MVP implementation
// (spec §6.2).
package rootfs

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// dockerManifest represents the manifest.json inside a docker-save tar.
// Only the fields we need are defined.
type dockerManifest []dockerManifestEntry

type dockerManifestEntry struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

// Extract reads a docker-save tar archive at tarPath and extracts its
// flattened rootfs into destDir. Layers are applied in manifest order
// (base first), with whiteouts processed per-layer.
func Extract(tarPath string, destDir string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", tarPath, err)
	}
	defer f.Close()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create dest dir %s: %w", destDir, err)
	}

	// Read the outer tar (docker-save format).
	// We need two passes: first to find manifest.json, then to extract
	// the layers referenced by the manifest. Since tar is sequential,
	// we collect all entries into memory keyed by cleaned path.
	outerTar := tar.NewReader(f)

	var manifest dockerManifest
	allEntries := make(map[string][]byte)

	for {
		hdr, err := outerTar.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read outer tar: %w", err)
		}

		name := filepath.Clean(hdr.Name)

		if name == "manifest.json" {
			data, err := io.ReadAll(outerTar)
			if err != nil {
				return fmt.Errorf("read manifest.json: %w", err)
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				return fmt.Errorf("parse manifest.json: %w", err)
			}
			continue
		}

		// Collect all regular file entries keyed by cleaned path.
		// We store them so we can later look up layers by manifest path.
		if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA {
			data, err := io.ReadAll(outerTar)
			if err != nil {
				return fmt.Errorf("read entry %s: %w", name, err)
			}
			allEntries[name] = data
		}
	}

	if len(manifest) == 0 {
		return fmt.Errorf("manifest.json not found in %s", tarPath)
	}

	// Process layers in manifest order.
	for _, layerPath := range manifest[0].Layers {
		cleanPath := filepath.Clean(layerPath)
		data, ok := allEntries[cleanPath]
		if !ok {
			return fmt.Errorf("layer %s not found in archive", layerPath)
		}

		if err := extractLayer(data, destDir); err != nil {
			return fmt.Errorf("extract layer %s: %w", layerPath, err)
		}
	}

	return nil
}

// extractLayer extracts a single layer tar (as raw bytes) into destDir,
// applying whiteout entries.
func extractLayer(layerBytes []byte, destDir string) error {
	r := newByteReader(layerBytes)
	tr := tar.NewReader(r)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read layer entry: %w", err)
		}

		name := filepath.Clean(hdr.Name)
		// Strip leading "./" or "/".
		name = strings.TrimPrefix(name, "./")
		name = strings.TrimPrefix(name, "/")

		if name == "" || name == "." {
			continue
		}

		// Handle whiteouts.
		base := filepath.Base(name)
		dir := filepath.Dir(name)

		if base == ".wh..wh..opq" {
			// Opaque whiteout: remove all entries in dir.
			fullDir := filepath.Join(destDir, dir)
			if err := removeDirContents(fullDir); err != nil {
				return fmt.Errorf("opaque whiteout %s: %w", name, err)
			}
			continue
		}

		if strings.HasPrefix(base, ".wh.") {
			// Named whiteout: remove the corresponding entry.
			targetName := strings.TrimPrefix(base, ".wh.")
			targetPath := filepath.Join(destDir, dir, targetName)
			os.RemoveAll(targetPath) // best-effort
			continue
		}

		// Normal entry — extract into destDir.
		targetPath := filepath.Join(destDir, name)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("mkdir %s: %w", name, err)
			}
			if err := restoreMetadata(targetPath, hdr, false); err != nil {
				return fmt.Errorf("restore directory metadata for %s: %w", name, err)
			}

		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return fmt.Errorf("create parent for %s: %w", name, err)
			}
			out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("create %s: %w", name, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("write %s: %w", name, err)
			}
			if err := out.Close(); err != nil {
				return fmt.Errorf("close %s: %w", name, err)
			}
			if err := restoreMetadata(targetPath, hdr, false); err != nil {
				return fmt.Errorf("restore file metadata for %s: %w", name, err)
			}

		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return fmt.Errorf("create parent for %s: %w", name, err)
			}
			os.Remove(targetPath)
			if err := os.Symlink(hdr.Linkname, targetPath); err != nil {
				return fmt.Errorf("symlink %s → %s: %w", name, hdr.Linkname, err)
			}
			if err := restoreMetadata(targetPath, hdr, true); err != nil {
				return fmt.Errorf("restore symlink metadata for %s: %w", name, err)
			}

		case tar.TypeLink:
			// Hard link: create a copy of the target file.
			linkSrc := filepath.Join(destDir, filepath.Clean(hdr.Linkname))
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return fmt.Errorf("create parent for %s: %w", name, err)
			}
			if err := copyFile(linkSrc, targetPath); err != nil {
				return fmt.Errorf("hardlink %s → %s: %w", name, hdr.Linkname, err)
			}
			if err := restoreMetadata(targetPath, hdr, false); err != nil {
				return fmt.Errorf("restore hardlink metadata for %s: %w", name, err)
			}

		default:
			// Skip unsupported types (sockets, fifos, devices, etc.)
		}
	}

	return nil
}

// restoreMetadata applies the numeric ownership and mode stored in a layer's
// tar header. Linux authorizes filesystem access using these IDs; names such
// as _apt are only /etc/passwd lookups. A rootful container runtime therefore
// must preserve the IDs exactly as recorded in the image.
func restoreMetadata(path string, hdr *tar.Header, symlink bool) error {
	if symlink {
		if err := os.Lchown(path, hdr.Uid, hdr.Gid); err != nil {
			return fmt.Errorf("lchown %d:%d: %w", hdr.Uid, hdr.Gid, err)
		}
		return nil
	}

	// Chown clears set-ID bits, so restore the mode afterwards.
	if err := os.Chown(path, hdr.Uid, hdr.Gid); err != nil {
		return fmt.Errorf("chown %d:%d: %w", hdr.Uid, hdr.Gid, err)
	}
	if err := os.Chmod(path, os.FileMode(hdr.Mode)); err != nil {
		return fmt.Errorf("chmod %04o: %w", hdr.Mode, err)
	}
	return nil
}

// removeDirContents removes all entries inside dir but keeps dir itself.
func removeDirContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies the contents of src to dst. Used for hard links.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// byteReader wraps a []byte as an io.Reader.
type byteReader struct {
	data []byte
	pos  int
}

func newByteReader(data []byte) *byteReader {
	return &byteReader{data: data}
}

func (b *byteReader) Read(p []byte) (int, error) {
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += n
	return n, nil
}
