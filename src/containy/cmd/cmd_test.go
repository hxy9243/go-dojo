package cmd

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
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
	flags := newRunFlagSet(&bytes.Buffer{}, &bytes.Buffer{}, &options)

	err := flags.Parse([]string{
		"-it",
		"--volume", "/tmp/hostdir:/data:ro",
		"--memory", "128m",
		"--cpu", "0.5",
		"--pids", "64",
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
	if options.memory != "128m" || options.cpu != "0.5" || options.pids != 64 {
		t.Errorf("limits parsed as memory=%q cpu=%q pids=%d", options.memory, options.cpu, options.pids)
	}
	if got := flags.Args(); len(got) != 4 || got[2] != "-c" {
		t.Errorf("positional arguments = %q, want command flags left untouched", got)
	}
}

func TestUsageIncludesRegisteredRunFlags(t *testing.T) {
	var output bytes.Buffer
	usage(&output)

	options := runOptions{}
	flags := newRunFlagSet(&bytes.Buffer{}, &bytes.Buffer{}, &options)
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
		{name: "valid rw directory", spec: "/tmp/hostdir:/data", wantDest: "/data"},
		{name: "valid ro file", spec: "/tmp/file.txt:/etc/file.txt:ro", wantDest: "/etc/file.txt", wantReadOnly: true},
		{name: "valid rw explicit", spec: "/tmp/hostdir:/data:rw", wantDest: "/data"},
		{name: "empty spec", spec: "", wantErr: true},
		{name: "no colon separator", spec: "/tmp/hostdir", wantErr: true},
		{name: "empty host path", spec: ":/data", wantErr: true},
		{name: "relative container path", spec: "/tmp/hostdir:data", wantErr: true},
		{name: "invalid mode", spec: "/tmp/hostdir:/data:invalid", wantErr: true},
		{name: "too many colons", spec: "/tmp/hostdir:/data:ro:extra", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mount, err := parseVolumeFlag(test.spec)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseVolumeFlag(%q) error = %v, wantErr %v", test.spec, err, test.wantErr)
			}
			if err == nil {
				if mount.Destination != test.wantDest || mount.ReadOnly != test.wantReadOnly {
					t.Errorf("parseVolumeFlag(%q) = %+v", test.spec, mount)
				}
				if mount.Source == "" {
					t.Error("expected non-empty source")
				}
			}
		})
	}
}

func TestPrepareRootfsDispatchesMissingImageReferenceToLocalDockerSave(t *testing.T) {
	sentinel := errors.New("save called")
	called := false
	_, _, err := prepareRootfsWithSave("ubuntu:latest", func(image, output string) error {
		called = true
		if image != "ubuntu:latest" || output == "" {
			t.Fatalf("saveImage(%q, %q)", image, output)
		}
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(output) != cwd {
			t.Errorf("saveImage output directory = %q, want %q", filepath.Dir(output), cwd)
		}
		return sentinel
	})
	if !called {
		t.Fatal("Docker save was not called")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want wrapped sentinel", err)
	}
}

func TestPrepareRootfsKeepsLocalImageArchive(t *testing.T) {
	var archivePath string
	_, _, err := prepareRootfsWithSave("ubuntu:latest", func(_, output string) error {
		archivePath = output
		return os.WriteFile(output, []byte("not a docker-save archive"), 0o600)
	})
	if err == nil {
		t.Fatal("prepareRootfsWithSave() error = nil, want extraction error")
	}
	if archivePath == "" {
		t.Fatal("saveImage was not called")
	}
	t.Cleanup(func() { _ = os.Remove(archivePath) })
	if _, statErr := os.Stat(archivePath); statErr != nil {
		t.Fatalf("local image archive was removed: %v", statErr)
	}
}

func TestPrepareRootfsDoesNotTreatMissingArchiveAsImage(t *testing.T) {
	called := false
	_, _, err := prepareRootfsWithSave("./missing.tar", func(_, _ string) error {
		called = true
		return nil
	})
	if called {
		t.Fatal("Docker save should not be called for an archive path")
	}
	if err == nil || !strings.Contains(err.Error(), "stat ./missing.tar") {
		t.Fatalf("error = %v, want missing archive error", err)
	}
}
