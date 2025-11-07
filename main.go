package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	DeviceIDConst = "vanpix_rpi"
)

var hostDiskPath string

var (
	lastDiscoveryPublish time.Time
)

const MinDiscoveryRepublishInterval = 30 * time.Second // debounce window

func env(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

type AppConfig struct {
	Broker        string
	User          string
	Password      string
	ClientID      string
	Prefix        string
	IntervalSec   int
	HAPrefix      string
	HostDiskPath  string
	HAIdentifiers []string
	DeviceID      string
	HADiscovery   bool
}

func loadConfig() AppConfig {
	intervalSec, _ := strconv.Atoi(env("INTERVAL_SECONDS", "30"))
	if intervalSec <= 0 {
		intervalSec = 30
	}
	user := env("MQTT_USER", "")
	pass := env("MQTT_PASSWORD", "")
	// If user is empty we ignore password (anonymous connection)
	if user == "" {
		pass = ""
	}
	// HA device identifiers: only read the plural env var; it can contain a single identifier as well
	ids := env("HA_DEVICE_IDENTIFIERS", "")
	deviceID := env("DEVICE_ID", DeviceIDConst)
	// discovery toggle (default true)
	haDisc := strings.ToLower(env("HA_DISCOVERY", "true")) != "false"
	return AppConfig{
		Broker:        env("MQTT_BROKER", "tcp://localhost:1883"),
		User:          user,
		Password:      pass,
		ClientID:      env("MQTT_CLIENT_ID", "vanpix_rpi"),
		Prefix:        env("MQTT_TOPIC_PREFIX", "vanpix_rpi"),
		IntervalSec:   intervalSec,
		HAPrefix:      env("HA_PREFIX", "homeassistant"),
		HostDiskPath:  env("HOST_ROOT_PATH", "/"),
		HAIdentifiers: parseIdentifiers(ids),
		DeviceID:      deviceID,
		HADiscovery:   haDisc,
	}
}

func parseIdentifiers(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// publishDeviceState publishes a single JSON with all metrics to <prefix>/state.
func publishDeviceState(client mqtt.Client, prefix string, s Stats) {
	b, err := json.Marshal(s)
	if err != nil {
		log.Printf("publish %s/state failed: %v", prefix, err)
		return
	}
	publish(client, fmt.Sprintf("%s/state", prefix), string(b))
}

func main() {
	cfg := loadConfig()
	hostDiskPath = cfg.HostDiskPath
	deviceID := cfg.DeviceID
	deviceIdentifiers := cfg.HAIdentifiers
	if len(deviceIdentifiers) == 0 {
		deviceIdentifiers = []string{deviceID}
	}

	// Use normalized prefix so users can set just "vanpi" and get "vanpi/sensor/<device>" topics.
	topicPrefix := normalizePrefix(cfg.Prefix, deviceID)
	availabilityTopic := fmt.Sprintf("%s/availability", topicPrefix)

	var wg sync.WaitGroup

	// Create MQTT client with encapsulated options and handlers
	client := NewMQTTClient(cfg, topicPrefix, deviceIdentifiers)

	// Retry connect with backoff (silent until final failure)
	maxAttempts := 10
	var connected bool
	var lastErr error
	for i := 1; i <= maxAttempts; i++ {
		if token := client.Connect(); token.Wait() && token.Error() == nil {
			log.Println("connected to mqtt broker")
			connected = true
			break
		} else {
			lastErr = token.Error()
			time.Sleep(time.Duration(i*2) * time.Second)
		}
	}
	if !connected && lastErr != nil {
		log.Printf("mqtt: failed to connect after %d attempts: %v", maxAttempts, lastErr)
	}

	ticker := time.NewTicker(time.Duration(cfg.IntervalSec) * time.Second)
	defer ticker.Stop()

	// Setup signal handling for graceful shutdown (Ctrl+C)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			wg.Add(1)
			go func() {
				defer wg.Done()
				report := gatherStats()
				publishDeviceState(client, topicPrefix, report)
			}()
		case sig := <-sigCh:
			log.Printf("received signal %v, shutting down", sig)
			ticker.Stop()
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				log.Println("timeout waiting for in-flight publishes")
			}
			// Mark device offline (explicit) before disconnect
			publish(client, availabilityTopic, "offline")
			if client != nil && client.IsConnected() {
				client.Disconnect(250)
			}
			return
		}
	}
}

func applyConfigToOptions(cfg AppConfig) *mqtt.ClientOptions {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(cfg.Broker)
	opts.SetClientID(cfg.ClientID)
	if cfg.User != "" { // only set creds if username provided
		opts.Username = cfg.User
		opts.Password = cfg.Password
	}
	return opts
}
