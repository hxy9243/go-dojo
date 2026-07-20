package cmd

import (
	"strings"
	"testing"
)

func TestFlagParsing(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantErr     bool
		errContains string
	}{
		{
			name:        "no args",
			args:        []string{},
			wantErr:     true,
			errContains: "no subcommand provided",
		},
		{
			name:        "help flag",
			args:        []string{"--help"},
			wantErr:     false,
		},
		{
			name:        "unknown subcommand",
			args:        []string{"foo"},
			wantErr:     true,
			errContains: "unknown subcommand \"foo\"",
		},
		{
			name:        "run missing required target",
			args:        []string{"run", "-it"},
			wantErr:     true,
			errContains: "run requires <image|tar|rootfs-dir>",
		},
		{
			name:        "run unknown flag",
			args:        []string{"run", "--unknown-flag", "ubuntu", "bash"},
			wantErr:     true,
			errContains: "unknown flag \"--unknown-flag\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Run(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Run() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Run() error = %v, want err containing %q", err, tt.errContains)
				}
			}
		})
	}
}
