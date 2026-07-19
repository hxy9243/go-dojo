// Package main is the entry point for the containy container runtime.
//
// containy is an educational container runtime. This minimal instance
// supports importing Docker images, extracting layered archives into a
// flat rootfs, and chrooting into it to run a command in the foreground.
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