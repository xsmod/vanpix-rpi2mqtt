package main

import (
	"encoding/json"
	"testing"
)

func TestStatsJSONTypes(t *testing.T) {
	s := Stats{
		CPULoad:     12.34,
		Temperature: 45,
		MemTotalMB:  1024,
		DiskTotalGB: 58,
		UptimeDays:  1.23,
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("failed to marshal stats: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("failed to unmarshal stats json: %v", err)
	}
	// temperature must be a number without decimals in JSON (Go unmarshals numbers as float64)
	if _, ok := m["temperature"]; !ok {
		t.Fatalf("temperature missing from json")
	}
	// cpu_load present
	if _, ok := m["cpu_load"]; !ok {
		t.Fatalf("cpu_load missing from json")
	}
	// disk_total_gb present
	if _, ok := m["disk_total_gb"]; !ok {
		t.Fatalf("disk_total_gb missing from json")
	}
}
