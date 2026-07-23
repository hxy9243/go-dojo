// Package docker wraps the docker CLI for image import operations.
//
// Containy uses `docker save` to produce a Docker-save tar archive from an
// image already present in Docker's local image store. This avoids depending
// on the Docker daemon's internal API and keeps the implementation small.
package docker

import (
	"fmt"
	"os/exec"
)

// Save runs `docker save <image> -o <outputTar>` and waits for it to
// complete. The output tar is in docker-save format (a tar of tars with
// a manifest.json).
func Save(image string, outputTar string) error {
	cmd := exec.Command("docker", "save", image, "-o", outputTar)
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker save %s: %w", image, err)
	}

	return nil
}
