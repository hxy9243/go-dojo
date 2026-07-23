package cmd

import "testing"

func TestParseMemoryLimit(t *testing.T) {
	tests := []struct {
		input   string
		want    uint64
		wantErr bool
	}{
		{input: "", want: 0},
		{input: "1024", want: 1024},
		{input: "128m", want: 128 << 20},
		{input: "2G", want: 2 << 30},
		{input: "0", wantErr: true},
		{input: "-1", wantErr: true},
		{input: "18446744073709551615g", wantErr: true},
	}
	for _, test := range tests {
		got, err := parseMemoryLimit(test.input)
		if (err != nil) != test.wantErr {
			t.Fatalf("parseMemoryLimit(%q) error = %v, wantErr %v", test.input, err, test.wantErr)
		}
		if err == nil && got != test.want {
			t.Errorf("parseMemoryLimit(%q) = %d, want %d", test.input, got, test.want)
		}
	}
}

func TestParseCPUQuota(t *testing.T) {
	tests := []struct {
		input   string
		want    uint64
		wantErr bool
	}{
		{input: "", want: 0},
		{input: "0.5", want: 50_000},
		{input: "2", want: 200_000},
		{input: "0.001", want: 1_000},
		{input: "0", wantErr: true},
		{input: "NaN", wantErr: true},
		{input: "+Inf", wantErr: true},
	}
	for _, test := range tests {
		got, err := parseCPUQuota(test.input)
		if (err != nil) != test.wantErr {
			t.Fatalf("parseCPUQuota(%q) error = %v, wantErr %v", test.input, err, test.wantErr)
		}
		if err == nil && got != test.want {
			t.Errorf("parseCPUQuota(%q) = %d, want %d", test.input, got, test.want)
		}
	}
}

func TestParseLimitsValidatesPids(t *testing.T) {
	for _, pids := range []uint64{0, 4_194_305} {
		if _, err := parseLimits(runOptions{pids: pids}); err == nil {
			t.Errorf("parseLimits(pids=%d) expected error", pids)
		}
	}
}
