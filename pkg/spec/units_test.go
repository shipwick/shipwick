package spec

import "testing"

func TestParseMemory(t *testing.T) {
	valid := map[string]int64{
		"128mb":    128 << 20,
		"512MB":    512 << 20,
		"1gb":      1 << 30,
		"1.5gb":    3 << 29,
		"2g":       2 << 30,
		"64m":      64 << 20,
		"256 mb":   256 << 20,
		"1gib":     1 << 30,
		"65536kb":  64 << 20,
		"10485760": 10 << 20,
	}
	for in, want := range valid {
		got, err := ParseMemory(in)
		if err != nil {
			t.Errorf("ParseMemory(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseMemory(%q) = %d, want %d", in, got, want)
		}
	}

	invalid := []string{"", "abc", "1tb", "-1gb", "1.2.3gb", "gb", "1mb", "5mb", "99999999999999gb", "0x10mb"}
	for _, in := range invalid {
		if _, err := ParseMemory(in); err == nil {
			t.Errorf("ParseMemory(%q): expected an error", in)
		}
	}
}

func TestFormatMemory(t *testing.T) {
	tests := map[int64]string{
		1 << 30:   "1 GB",
		3 << 29:   "1.5 GB",
		412 << 20: "412 MB",
		2048:      "2 KB",
		100:       "100 B",
	}
	for in, want := range tests {
		if got := FormatMemory(in); got != want {
			t.Errorf("FormatMemory(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestParseCPU(t *testing.T) {
	valid := map[string]float64{"2": 2, "0.5": 0.5, "0.25": 0.25, " 4 ": 4, "1.0": 1}
	for in, want := range valid {
		got, err := ParseCPU(in)
		if err != nil || got != want {
			t.Errorf("ParseCPU(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "0", "-1", "0.001", "9999", "NaN", "Inf"} {
		if _, err := ParseCPU(in); err == nil {
			t.Errorf("ParseCPU(%q): expected an error", in)
		}
	}
}
