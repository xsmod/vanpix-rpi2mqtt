package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestBuildDiscoveryConfigs_DefaultIdentifiers(t *testing.T) {
	cfg := AppConfig{
		Prefix:   "vanpi",
		HAPrefix: "homeassistant",
		// HAIdentifiers empty -> should trigger metadata
	}
	expectedDevice := "vanpix_rpi"
	pairs := buildDiscoveryConfigs(cfg, expectedDevice, "device-identifier")
	if len(pairs) == 0 {
		t.Fatalf("expected non-empty discovery pairs")
	}
	// Expect number of pairs equal to number of templates
	tmplCount := len(getComponentTemplates("state/topic"))
	if len(pairs) != tmplCount {
		t.Fatalf("expected %d discovery pairs, got %d", tmplCount, len(pairs))
	}

	// Check each payload includes device metadata and identifiers with DeviceID first
	for _, p := range pairs {
		if !strings.HasPrefix(p.topic, cfg.HAPrefix+"/sensor/") {
			t.Fatalf("unexpected topic prefix: %s", p.topic)
		}
		if !strings.HasSuffix(p.topic, "/config") {
			t.Fatalf("topic must end with /config: %s", p.topic)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(p.payload), &payload); err != nil {
			t.Fatalf("invalid json payload: %v", err)
		}
		devI, ok := payload["device"]
		if !ok {
			t.Fatalf("payload missing device object")
		}
		devm, ok := devI.(map[string]interface{})
		if !ok {
			t.Fatalf("device is not object")
		}
		// name/manufacturer/model should be present when no HA identifiers configured
		if devm["name"] == nil {
			t.Fatalf("expected device.name present when no HA identifiers")
		}
		if devm["manufacturer"] == nil || devm["model"] == nil {
			t.Fatalf("expected device.manufacturer and device.model present")
		}
		ids, ok := devm["identifiers"].([]interface{})
		if !ok || len(ids) == 0 {
			t.Fatalf("device.identifiers missing or empty")
		}
		first := fmt.Sprintf("%v", ids[0])
		if first != expectedDevice {
			t.Fatalf("expected first identifier to be %s, got %s", expectedDevice, first)
		}
	}
}

func TestBuildDiscoveryConfigs_WithHAIdentifiers(t *testing.T) {
	cfg := AppConfig{
		Prefix:        "vanpi",
		HAPrefix:      "homeassistant",
		HAIdentifiers: []string{"serial123", "mac:AA:BB:CC"},
	}
	expectedDevice := "vanpix_rpi"
	pairs := buildDiscoveryConfigs(cfg, expectedDevice, "device-identifier")
	if len(pairs) == 0 {
		t.Fatalf("expected non-empty discovery pairs")
	}

	for _, p := range pairs {
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(p.payload), &payload); err != nil {
			t.Fatalf("invalid json payload: %v", err)
		}
		devI, ok := payload["device"]
		if !ok {
			t.Fatalf("payload missing device object")
		}
		devm := devI.(map[string]interface{})
		// name should be omitted when HA identifiers provided
		if _, ok := devm["name"]; ok {
			t.Fatalf("did not expect device.name when HA identifiers provided")
		}
		ids, ok := devm["identifiers"].([]interface{})
		if !ok {
			t.Fatalf("device.identifiers missing or wrong type")
		}
		foundDeviceID := false
		foundSerial := false
		for _, v := range ids {
			s := fmt.Sprintf("%v", v)
			if s == expectedDevice {
				foundDeviceID = true
			}
			if s == "serial123" {
				foundSerial = true
			}
		}
		if !foundDeviceID || !foundSerial {
			t.Fatalf("expected identifiers to include %s and serial123, got %v", expectedDevice, ids)
		}
	}
}

func TestBuildDiscoveryConfigs_UsesDeviceIDInTopics(t *testing.T) {
	cfg := AppConfig{
		Prefix:   "vanpi",
		HAPrefix: "homeassistant",
	}
	deviceID := "Custom-Device.1"
	pairs := buildDiscoveryConfigs(cfg, deviceID, "dev-ident")
	if len(pairs) == 0 {
		t.Fatalf("expected discovery pairs, got none")
	}
	deviceSeg := sanitizeObjectID(deviceID)
	for _, p := range pairs {
		want := fmt.Sprintf("%s/sensor/%s/", cfg.HAPrefix, deviceSeg)
		if !strings.Contains(p.topic, want) {
			t.Fatalf("topic %q does not contain expected device segment %q", p.topic, want)
		}
	}
}

func TestDiscoveryPayloads_NoUniqueID_AndDeviceSegment(t *testing.T) {
	cfg := AppConfig{Prefix: "vanpi", HAPrefix: "homeassistant"}
	deviceID := "My-Device.01"
	pairs := buildDiscoveryConfigs(cfg, deviceID, "dev-ident")
	if len(pairs) == 0 {
		t.Fatalf("expected discovery pairs, got none")
	}
	deviceSeg := sanitizeObjectID(deviceID)
	for _, p := range pairs {
		// topic must contain device segment
		if !strings.Contains(p.topic, "/sensor/"+deviceSeg+"/") {
			t.Fatalf("topic %q missing device segment %q", p.topic, deviceSeg)
		}
		// payload must not contain unique_id
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(p.payload), &payload); err != nil {
			t.Fatalf("invalid json payload: %v", err)
		}
		if _, ok := payload["unique_id"]; ok {
			t.Fatalf("expected payload to not include unique_id, got %v", payload)
		}
		// device identifiers must include deviceID as first entry
		devI, _ := payload["device"]
		devm := devI.(map[string]interface{})
		ids := devm["identifiers"].([]interface{})
		first := ids[0].(string)
		if first != deviceID {
			t.Fatalf("expected first identifier %s, got %s", deviceID, first)
		}
	}
}
