package main

import (
	"log"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// NewMQTTClient creates and configures an MQTT client using the provided AppConfig.
// It sets Last Will and OnConnect behavior (availability + discovery if enabled).
func NewMQTTClient(cfg AppConfig, topicPrefix string, deviceIdentifiers []string) mqtt.Client {
	opts := applyConfigToOptions(cfg)
	// Last will
	opts.SetWill(topicPrefix+"/availability", "offline", 0, true)
	// OnConnect behavior
	opts.OnConnect = func(c mqtt.Client) {
		publish(c, topicPrefix+"/availability", "online")
		if cfg.HADiscovery {
			// Debounce discovery publishes
			if time.Since(lastDiscoveryPublish) >= MinDiscoveryRepublishInterval {
				publishDeviceDiscovery(c, cfg, deviceIdentifiers[0])
				lastDiscoveryPublish = time.Now()
			} else {
				log.Printf("skip discovery publish (debounced, %v since last)", time.Since(lastDiscoveryPublish))
			}
		}
	}
	return mqtt.NewClient(opts)
}

// publish is a resilient publish helper used by the client.
// It will try to connect the client if disconnected and wait for publish completion.
func publish(client mqtt.Client, topic, payload string) {
	if client == nil {
		log.Printf("mqtt client nil, cannot publish %s", topic)
		return
	}
	if !client.IsConnected() {
		if token := client.Connect(); token != nil {
			_ = token.WaitTimeout(2 * time.Second)
		}
	}
	if !client.IsConnected() {
		log.Printf("publish %s skipped: mqtt disconnected", topic)
		return
	}
	t := client.Publish(topic, 0, true, payload)
	if ok := t.WaitTimeout(5 * time.Second); !ok {
		log.Printf("publish %s failed: timeout", topic)
		return
	}
	if err := t.Error(); err != nil {
		log.Printf("publish %s failed: %v", topic, err)
		return
	}
	log.Printf("published %s", topic)
}
