#!/usr/bin/env bash
#
# oci-to-rootfs.sh — Flatten an OCI/Docker image layout into a single
# rootfs tar archive suitable for `containy run`.
#
# Usage:
#   ./oci-to-rootfs.sh <oci-image-dir> <output-rootfs.tar>
#
# The OCI image dir must contain:
#   - manifest.json   (Docker save format) or
#   - index.json      (OCI image layout format)
# In both cases the layer blobs are referenced under blobs/sha256/.
#
# This script:
#   1. Reads the manifest to determine layer order.
#   2. Extracts each layer tar in order into a staging directory.
#   3. Applies OCI whiteouts (entries named .wh.<name> or .wh..wh..opq).
#   4. Re-tars the merged result as a flat rootfs archive.
#
# The output is a plain tar (no compression) whose entries are relative
# paths like "bin/", "etc/passwd", etc. — exactly what containy's
# internal/rootfs extractor expects per §6.2 of the spec.

set -euo pipefail

if [ "$#" -ne 2 ]; then
    echo "Usage: $0 <oci-image-dir> <output-rootfs.tar>" >&2
    exit 2
fi

IMAGE_DIR="$1"
OUTPUT_TAR="$2"

if [ ! -d "$IMAGE_DIR" ]; then
    echo "error: image directory '$IMAGE_DIR' not found" >&2
    exit 1
fi

BLOB_DIR="$IMAGE_DIR/blobs/sha256"
if [ ! -d "$BLOB_DIR" ]; then
    echo "error: blob directory '$BLOB_DIR' not found" >&2
    exit 1
fi

# --- Resolve layer list -------------------------------------------------------
#
# Docker save layout:  manifest.json is an array; each entry has "Layers".
# OCI layout:          index.json points to a manifest blob; that blob has
#                      "layers" with "digest" fields.
#
# We try Docker save first, then OCI.

LAYERS=()

if [ -f "$IMAGE_DIR/manifest.json" ]; then
    # Docker save format — manifest.json is an array of objects.
    # Extract layer paths with jq. Each path is relative to IMAGE_DIR.
    mapfile -t LAYERS < <(
        jq -r '.[0].Layers[]' "$IMAGE_DIR/manifest.json" \
        | while IFS= read -r layer; do
            # Resolve to absolute path
            if [[ "$layer" == /* ]]; then
                echo "$layer"
            else
                echo "$IMAGE_DIR/$layer"
            fi
        done
    )
elif [ -f "$IMAGE_DIR/index.json" ]; then
    # OCI image layout — follow index.json -> manifest blob -> layers[]
    MANIFEST_DIGEST=$(jq -r '.manifests[0].digest' "$IMAGE_DIR/index.json")
    MANIFEST_DIGEST="${MANIFEST_DIGEST#sha256:}"
    MANIFEST_BLOB="$BLOB_DIR/$MANIFEST_DIGEST"

    if [ ! -f "$MANIFEST_BLOB" ]; then
        echo "error: manifest blob '$MANIFEST_BLOB' not found" >&2
        exit 1
    fi

    mapfile -t LAYERS < <(
        jq -r '.layers[].digest' "$MANIFEST_BLOB" \
        | while IFS= read -r digest; do
            digest="${digest#sha256:}"
            echo "$BLOB_DIR/$digest"
        done
    )
else
    echo "error: neither manifest.json (Docker save) nor index.json (OCI) found" >&2
    exit 1
fi

if [ "${#LAYERS[@]}" -eq 0 ]; then
    echo "error: no layers found in manifest" >&2
    exit 1
fi

echo "Found ${#LAYERS[@]} layer(s):"
for layer in "${LAYERS[@]}"; do
    echo "  $layer"
done

# --- Merge layers into a staging directory -----------------------------------

STAGING_DIR=$(mktemp -d)
trap 'rm -rf "$STAGING_DIR"' EXIT

echo "Merging layers into $STAGING_DIR ..."

for layer in "${LAYERS[@]}"; do
    if [ ! -f "$layer" ]; then
        echo "error: layer '$layer' not found" >&2
        exit 1
    fi

    # Extract this layer into the staging dir.
    # We handle whiteouts in a second pass.
    tar -xf "$layer" -C "$STAGING_DIR" 2>/dev/null || true

    # Process whiteouts:
    #   .wh.<name>   — remove <name> from the parent directory
    #   .wh..wh..opq — remove everything in the parent directory
    # (This image has none, but the script is general-purpose.)
    while IFS= read -r wh_entry; do
        wh_dir=$(dirname "$wh_entry")
        wh_base=$(basename "$wh_entry")

        if [ "$wh_base" = ".wh..wh..opq" ]; then
            # Opaque whiteout: remove all existing entries in wh_dir
            # (but not the whiteout file itself yet).
            find "$STAGING_DIR/$wh_dir" -mindepth 1 -maxdepth 1 \
                ! -name '.wh..wh..opq' -exec rm -rf {} + 2>/dev/null || true
        elif [[ "$wh_base" == .wh.* ]]; then
            # Named whiteout: remove the corresponding entry
            target_name="${wh_base#.wh.}"
            rm -rf "$STAGING_DIR/$wh_dir/$target_name" 2>/dev/null || true
        fi

        # Remove the whiteout marker itself
        rm -f "$STAGING_DIR/$wh_entry" 2>/dev/null || true
    done < <(tar -tf "$layer" 2>/dev/null | grep '\.wh\.' || true)
done

echo "Merged rootfs contains $(find "$STAGING_DIR" -mindepth 1 | wc -l) entries."

# --- Create the flat rootfs tar -----------------------------------------------

echo "Creating $OUTPUT_TAR ..."

# tar from inside the staging dir so paths are relative (bin/, etc/, ...)
tar -cf "$OUTPUT_TAR" -C "$STAGING_DIR" .

echo "Done. Output: $OUTPUT_TAR ($(du -h "$OUTPUT_TAR" | cut -f1))"