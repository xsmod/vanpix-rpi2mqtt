package main

import (
	"testing"
)

func TestParseLoadAvgFromBytes(t *testing.T) {
	b := []byte("0.12 0.34 0.56 1/234 5678\n")
	got := parseLoadAvgFromBytes(b)
	want := "0.12"
	if got != want {
		t.Fatalf("parseLoadAvgFromBytes() = %q; want %q", got, want)
	}

	// empty
	if got := parseLoadAvgFromBytes([]byte("")); got != "" {
		t.Fatalf("expected empty for empty input, got %q", got)
	}
}

func TestParseUptimeFromBytes(t *testing.T) {
	b := []byte("12345.67 89012.34\n")
	if got := parseUptimeFromBytes(b); got != 12345 {
		t.Fatalf("parseUptimeFromBytes() = %d; want %d", got, 12345)
	}

	// invalid
	if got := parseUptimeFromBytes([]byte("notanumber")); got != 0 {
		t.Fatalf("expected 0 for invalid uptime, got %d", got)
	}
}

func TestParseUptimeSecondsFromBytes(t *testing.T) {
	b := []byte("12345.67 89012.34\n")
	if got := parseUptimeSecondsFromBytes(b); got < 12345.66 || got > 12345.68 {
		t.Fatalf("parseUptimeSecondsFromBytes() = %f; want around 12345.67", got)
	}

	if got := parseUptimeSecondsFromBytes([]byte("")); got != 0 {
		t.Fatalf("expected 0 for empty input, got %f", got)
	}

	if got := parseUptimeSecondsFromBytes([]byte("bogus")); got != 0 {
		t.Fatalf("expected 0 for invalid input, got %f", got)
	}
}

func TestParseMemInfoFromBytes(t *testing.T) {
	input := `MemTotal:       16384256 kB
MemFree:         1234567 kB
MemAvailable:    2345678 kB
Buffers:           12345 kB
Cached:           543210 kB
`
	m := parseMemInfoFromBytes([]byte(input))
	if m == nil {
		t.Fatalf("parseMemInfoFromBytes returned nil")
	}
	if got := m["MemTotal"]; got != 16384256 {
		t.Fatalf("MemTotal = %d; want %d", got, 16384256)
	}
	if got := m["MemFree"]; got != 1234567 {
		t.Fatalf("MemFree = %d; want %d", got, 1234567)
	}
	if got := m["MemAvailable"]; got != 2345678 {
		t.Fatalf("MemAvailable = %d; want %d", got, 2345678)
	}

	// missing values
	m2 := parseMemInfoFromBytes([]byte(""))
	if m2 == nil {
		// expected empty map not nil
		t.Fatalf("expected empty map not nil")
	}
	if len(m2) != 0 {
		t.Fatalf("expected empty map for empty input, got len=%d", len(m2))
	}
}
