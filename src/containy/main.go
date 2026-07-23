// Package main is the entry point for the containy container runtime.
//
// containy is an educational container runtime. It imports Docker images,
// prepares a flat rootfs, and runs a foreground workload in Linux namespaces
// with cgroups v2 resource limits.
package main

import (
	"fmt"
	"os"

	"github.com/hxy9243/go-dojo/containy/cmd"
)

func main() {
	if err := cmd.Run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "containy: %v\n", err)
		os.Exit(1)
	}
}
