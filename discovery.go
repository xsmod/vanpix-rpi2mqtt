package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// getComponentTemplates returns the per-sensor template map.
func getComponentTemplates(stateTopic string) map[string]map[string]interface{} {
	return map[string]map[string]interface{}{
		"cpu_load": {
			"name":                "CPU Load",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.cpu_load }}",
			"unit_of_measurement": "%",
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"temperature": {
			"name":                "Temperature",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.temperature }}",
			"device_class":        "temperature",
			"unit_of_measurement": "°C",
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"mem_total_mb": {
			"name":                "Memory Total",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.mem_total_mb }}",
			"unit_of_measurement": "MB",
			"device_class":        "data_size",
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"mem_available_mb": {
			"name":                "Memory Available",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.mem_available_mb }}",
			"unit_of_measurement": "MB",
			"device_class":        "data_size",
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"mem_free_mb": {
			"name":                "Memory Free",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.mem_free_mb }}",
			"unit_of_measurement": "MB",
			"device_class":        "data_size",
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"disk_total_gb": {
			"name":                "Disk Total",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.disk_total_gb }}",
			"unit_of_measurement": "GB",
			"device_class":        "data_size",
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"disk_free_gb": {
			"name":                "Disk Free",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.disk_free_gb }}",
			"unit_of_measurement": "GB",
			"device_class":        "data_size",
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"uptime_days": {
			"name":                "Uptime Days",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.uptime_days }}",
			"unit_of_measurement": "d",
			"device_class":        "duration",
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
	}
}

// buildIdentifiers returns a deduplicated slice of identifiers with deviceID first.
func buildIdentifiers(cfg AppConfig, deviceID, deviceIdentifier string) []string {
	identMap := map[string]bool{}
	identifiers := make([]string, 0, 1+len(cfg.HAIdentifiers))
	addIdent := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if !identMap[id] {
			identMap[id] = true
			identifiers = append(identifiers, id)
		}
	}
	addIdent(deviceID)
	for _, id := range cfg.HAIdentifiers {
		addIdent(id)
	}
	addIdent(deviceIdentifier)
	return identifiers
}

// buildDeviceObj returns the device object to inject into discovery payloads.
func buildDeviceObj(cfg AppConfig, deviceID, deviceIdentifier string) map[string]interface{} {
	identifiers := buildIdentifiers(cfg, deviceID, deviceIdentifier)
	dev := map[string]interface{}{"identifiers": identifiers}
	if len(cfg.HAIdentifiers) == 0 {
		dev["name"] = "VanPIX RPI"
		dev["manufacturer"] = "github.com/xsmod"
		dev["model"] = "vanpix-rpi2mqtt"
	}
	return dev
}

// buildDiscoveryConfigs returns a slice of (topic,payload) pairs ready to publish for HA discovery.
// It does not perform the publish itself, making it easy to unit-test.
func buildDiscoveryConfigs(cfg AppConfig, deviceID, deviceIdentifier string) []struct{ topic, payload string } {
	basePrefix := normalizePrefix(cfg.Prefix, deviceID)
	availabilityTopic := fmt.Sprintf("%s/availability", basePrefix)
	stateTopic := fmt.Sprintf("%s/state", basePrefix)

	components := getComponentTemplates(stateTopic)
	deviceObj := buildDeviceObj(cfg, deviceID, deviceIdentifier)

	out := make([]struct{ topic, payload string }, 0, len(components))
	for key, tmpl := range components {
		m := map[string]interface{}{}
		for k, v := range tmpl {
			m[k] = v
		}
		m["device"] = deviceObj
		m["availability_topic"] = availabilityTopic
		m["payload_available"] = "online"
		m["payload_not_available"] = "offline"
		m["qos"] = 2
		m["retain"] = true

		// unique_id removed: not required

		deviceSeg := sanitizeObjectID(deviceID)
		sensorSeg := sanitizeObjectID(key)
		payload, _ := json.Marshal(m)
		topic := fmt.Sprintf("%s/sensor/%s/%s/config", cfg.HAPrefix, deviceSeg, sensorSeg)
		out = append(out, struct{ topic, payload string }{topic: topic, payload: string(payload)})
	}
	return out
}

// publishDeviceDiscovery publishes the discovery configs to MQTT.
func publishDeviceDiscovery(client mqtt.Client, cfg AppConfig, deviceIdentifier string) {
	// Always use the configured device id when building discovery payloads.
	// The device id is included as the first identifier in every discovery payload.
	log.Printf("publishing discovery using device id %s", cfg.DeviceID)
	pairs := buildDiscoveryConfigs(cfg, cfg.DeviceID, deviceIdentifier)
	for _, p := range pairs {
		client.Publish(p.topic, 0, true, p.payload).Wait()
		log.Printf("published %s", p.topic)
	}
}
