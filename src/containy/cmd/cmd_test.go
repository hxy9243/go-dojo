package cmd

import (
	"bytes"
	"flag"
	"io"
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
			name:    "help flag",
			args:    []string{"--help"},
			wantErr: false,
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
		{
			name:        "run volume flag missing argument",
			args:        []string{"run", "-v"},
			wantErr:     true,
			errContains: "flag -v/--volume requires an argument",
		},
		{
			name:        "run volume flag invalid spec",
			args:        []string{"run", "-v", "/host:data", "ubuntu", "bash"},
			wantErr:     true,
			errContains: "invalid volume specification",
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

func TestRunFlagSet(t *testing.T) {
	options := runOptions{}
	flags := newRunFlagSet(io.Discard, io.Discard, &options)

	err := flags.Parse([]string{
		"-it",
		"--volume", "/tmp/hostdir:/data:ro",
		"rootfs", "sh", "-c", "echo hello",
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !options.interactive || !options.tty {
		t.Errorf("-it parsed as interactive=%v, tty=%v", options.interactive, options.tty)
	}
	if len(options.mounts) != 1 || options.mounts[0].Destination != "/data" || !options.mounts[0].ReadOnly {
		t.Errorf("--volume parsed as %+v", options.mounts)
	}
	if got := flags.Args(); len(got) != 4 || got[2] != "-c" {
		t.Errorf("positional arguments = %q, want command flags left untouched", got)
	}
}

func TestUsageIncludesRegisteredRunFlags(t *testing.T) {
	var output bytes.Buffer
	usage(&output)

	options := runOptions{}
	flags := newRunFlagSet(io.Discard, io.Discard, &options)
	flags.VisitAll(func(registered *flag.Flag) {
		if !strings.Contains(output.String(), "-"+registered.Name) {
			t.Errorf("usage does not include registered flag -%s", registered.Name)
		}
	})
}

func TestParseVolumeFlag(t *testing.T) {
	tests := []struct {
		name         string
		spec         string
		wantDest     string
		wantReadOnly bool
		wantErr      bool
	}{
		{
			name:         "valid rw directory",
			spec:         "/tmp/hostdir:/data",
			wantDest:     "/data",
			wantReadOnly: false,
			wantErr:      false,
		},
		{
			name:         "valid ro file",
			spec:         "/tmp/file.txt:/etc/file.txt:ro",
			wantDest:     "/etc/file.txt",
			wantReadOnly: true,
			wantErr:      false,
		},
		{
			name:         "valid rw explicit",
			spec:         "/tmp/hostdir:/data:rw",
			wantDest:     "/data",
			wantReadOnly: false,
			wantErr:      false,
		},
		{
			name:    "empty spec",
			spec:    "",
			wantErr: true,
		},
		{
			name:    "no colon separator",
			spec:    "/tmp/hostdir",
			wantErr: true,
		},
		{
			name:    "empty host path",
			spec:    ":/data",
			wantErr: true,
		},
		{
			name:    "relative container path",
			spec:    "/tmp/hostdir:data",
			wantErr: true,
		},
		{
			name:    "invalid mode",
			spec:    "/tmp/hostdir:/data:invalid",
			wantErr: true,
		},
		{
			name:    "too many colons",
			spec:    "/tmp/hostdir:/data:ro:extra",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := parseVolumeFlag(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseVolumeFlag(%q) error = %v, wantErr %v", tt.spec, err, tt.wantErr)
			}
			if err == nil {
				if m.Destination != tt.wantDest {
					t.Errorf("Destination = %q, want %q", m.Destination, tt.wantDest)
				}
				if m.ReadOnly != tt.wantReadOnly {
					t.Errorf("ReadOnly = %v, want %v", m.ReadOnly, tt.wantReadOnly)
				}
				if m.Source == "" {
					t.Errorf("expected non-empty Source path")
				}
			}
		})
	}
}
