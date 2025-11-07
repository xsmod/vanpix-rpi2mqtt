package main

import (
	"fmt"
	"strings"
	"unicode"
)

// normalizePrefix turns a simple namespace like "vanpi" into
// "vanpi/sensor/<deviceID>" so topics remain well-scoped.
// Rules:
// - Trim trailing slashes
// - If resulting prefix has no slash, append "/sensor/<deviceID>"
// - If prefix contains "/sensor", replace any trailing segment with deviceID
func normalizePrefix(prefix, deviceID string) string {
	p := strings.TrimSuffix(prefix, "/")
	if strings.Contains(p, "/sensor") {
		parts := strings.SplitN(p, "/sensor", 2)
		base := strings.TrimSuffix(parts[0], "/")
		return fmt.Sprintf("%s/sensor/%s", base, deviceID)
	}
	if !strings.Contains(p, "/") {
		return fmt.Sprintf("%s/sensor/%s", p, deviceID)
	}
	return p
}

// sanitizeObjectID lowercases and replaces non-alphanumeric (except underscore) with underscores.
// Use this when generating HA object_id / unique_id to ensure valid, stable names.
func sanitizeObjectID(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return unicode.ToLower(r)
		}
		return '_'
	}, s)
}
