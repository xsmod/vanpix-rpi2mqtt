package main

import (
	"testing"
)

func TestSanitizeObjectID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"My-Device.01", "my_device_01"},
		{"vanpix_rpi", "vanpix_rpi"},
		{"UPPER CASE", "upper_case"},
		{"sp€cial", "sp_cial"},
	}
	for _, c := range cases {
		got := sanitizeObjectID(c.in)
		if got != c.want {
			t.Fatalf("sanitizeObjectID(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}
