package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"math"
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

// Protect previous CPU counters for percent calculation across intervals
var cpuMu sync.Mutex
var prevCPUTotal uint64
var prevCPUIdle uint64
var prevCPUSet bool

var (
	// BuildTag and BuildCommit can be set via -ldflags at build time:
	//   -ldflags "-X main.BuildTag=v1.2.3 -X main.BuildCommit=abcdef1"
	BuildTag             string
	BuildCommit          string
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
	SWVersion     string
	HADiscovery   bool
}

func getSWVersion() string {
	// Allow explicit override via APP_VERSION for special cases
	if v := os.Getenv("APP_VERSION"); v != "" {
		return v
	}
	if BuildTag != "" {
		return BuildTag
	}
	if BuildCommit != "" {
		if len(BuildCommit) >= 7 {
			return BuildCommit[:7]
		}
		return BuildCommit
	}
	return "dev"
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
	// HA device identifiers: support both singular and plural envs
	ids := env("HA_DEVICE_IDENTIFIERS", "")
	if ids == "" {
		ids = env("HA_DEVICE_IDENTIFIER", "")
	}
	// discovery toggle (default true)
	haDisc := strings.ToLower(env("HA_DISCOVERY", "true")) != "false"
	sw := getSWVersion()
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
		SWVersion:     sw,
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

// normalizePrefix turns a simple namespace like "vanpi" into
// "vanpi/sensor/<deviceID>" so topics remain well-scoped.
// Rules:
// - Trim trailing slashes
// - If resulting prefix has no slash, append "/sensor/<deviceID>"
// - Otherwise, leave as-is
func normalizePrefix(prefix, deviceID string) string {
	p := strings.TrimSuffix(prefix, "/")
	if !strings.Contains(p, "/") {
		return fmt.Sprintf("%s/sensor/%s", p, deviceID)
	}
	return p
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

func main() {
	cfg := loadConfig()
	hostDiskPath = cfg.HostDiskPath
	deviceID := DeviceIDConst
	deviceIdentifiers := cfg.HAIdentifiers
	if len(deviceIdentifiers) == 0 {
		deviceIdentifiers = []string{deviceID}
	}

	// Use normalized prefix so users can set just "vanpi" and get "vanpi/sensor/<device>" topics.
	topicPrefix := normalizePrefix(cfg.Prefix, deviceID)
	availabilityTopic := fmt.Sprintf("%s/availability", topicPrefix)

	var wg sync.WaitGroup

	opts := applyConfigToOptions(cfg)
	// Set MQTT Last Will to offline so broker marks availability on ungraceful exit
	opts.SetWill(availabilityTopic, "offline", 0, true)
	// On every successful (re)connect publish availability online and discovery if enabled
	opts.OnConnect = func(c mqtt.Client) {
		publish(c, availabilityTopic, "online")
		if cfg.HADiscovery {
			// Debounce: publish discovery only if last publish older than window
			if time.Since(lastDiscoveryPublish) >= MinDiscoveryRepublishInterval {
				publishDeviceDiscovery(c, cfg, deviceIdentifiers[0])
				lastDiscoveryPublish = time.Now()
			} else {
				log.Printf("skip discovery publish (debounced, %v since last)", time.Since(lastDiscoveryPublish))
			}
		}
	}
	client := mqtt.NewClient(opts)

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

// publishDeviceState publishes a single JSON with all metrics to <prefix>/state.
func publishDeviceState(client mqtt.Client, prefix string, s Stats) {
	b, err := json.Marshal(s)
	if err != nil {
		log.Printf("publish %s/state failed: %v", prefix, err)
		return
	}
	publish(client, fmt.Sprintf("%s/state", prefix), string(b))
}

type Stats struct {
	CPULoad        float64 `json:"cpu_load"` // percent 0-100
	Temperature    float64 `json:"temperature"`
	MemTotalMB     int64   `json:"mem_total_mb"`
	MemAvailableMB int64   `json:"mem_available_mb"`
	MemFreeMB      int64   `json:"mem_free_mb"`
	DiskTotalGB    int64   `json:"disk_total_gb"`
	DiskFreeGB     int64   `json:"disk_free_gb"`
	UptimeDays     float64 `json:"uptime_days"`
	IP             string  `json:"ip"`
}

func gatherStats() Stats {
	c := getTemperature()
	ud := getUptimeDays()
	ud = math.Round(ud*100) / 100 // round to 2 decimals for JSON output
	cp := getCPUPercent()
	cp = math.Round(cp*100) / 100 // round CPU percent to 2 decimals
	// floor disk sizes to whole GB
	dtot := int64(math.Floor(getDiskGB(hostDiskPath)))
	dfree := int64(math.Floor(getDiskFreeGB(hostDiskPath)))
	return Stats{
		CPULoad:        cp,
		Temperature:    c,
		MemTotalMB:     getMemTotalMB(),
		MemAvailableMB: getMemAvailableMB(),
		MemFreeMB:      getMemFreeMB(),
		DiskTotalGB:    dtot,
		DiskFreeGB:     dfree,
		UptimeDays:     ud,
		IP:             getConfiguredIP(),
	}
}

// publishDeviceDiscovery publishes a single device discovery JSON following the requested schema.
func publishDeviceDiscovery(client mqtt.Client, cfg AppConfig, deviceIdentifier string) {
	// Use normalized prefix inside discovery as well
	basePrefix := normalizePrefix(cfg.Prefix, DeviceIDConst)
	availabilityTopic := fmt.Sprintf("%s/availability", basePrefix)
	stateTopic := fmt.Sprintf("%s/state", basePrefix)

	components := map[string]map[string]interface{}{
		"cpu_load": {
			"unique_id":           fmt.Sprintf("%s_cpu_load", DeviceIDConst),
			"name":                "CPU Load",
			"platform":            "sensor",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.cpu_load }}",
			"unit_of_measurement": "%",
			"retain":              true,
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"temperature": {
			"unique_id":       fmt.Sprintf("%s_temperature", DeviceIDConst),
			"name":            "Temperature",
			"platform":        "sensor",
			"state_topic":     stateTopic,
			"value_template":  "{{ value_json.temperature }}",
			"device_class":    "temperature",
			"retain":          true,
			"state_class":     "measurement",
			"entity_category": "diagnostic",
		},
		"mem_total_mb": {
			"unique_id":           fmt.Sprintf("%s_mem_total_mb", DeviceIDConst),
			"name":                "Memory Total",
			"platform":            "sensor",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.mem_total_mb }}",
			"unit_of_measurement": "MB",
			"device_class":        "data_size",
			"retain":              true,
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"mem_available_mb": {
			"unique_id":           fmt.Sprintf("%s_mem_available_mb", DeviceIDConst),
			"name":                "Memory Available",
			"platform":            "sensor",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.mem_available_mb }}",
			"unit_of_measurement": "MB",
			"device_class":        "data_size",
			"retain":              true,
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"mem_free_mb": {
			"unique_id":           fmt.Sprintf("%s_mem_free_mb", DeviceIDConst),
			"name":                "Memory Free",
			"platform":            "sensor",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.mem_free_mb }}",
			"unit_of_measurement": "MB",
			"device_class":        "data_size",
			"retain":              true,
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"disk_total_gb": {
			"unique_id":           fmt.Sprintf("%s_disk_total_gb", DeviceIDConst),
			"name":                "Disk Total",
			"platform":            "sensor",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.disk_total_gb }}",
			"unit_of_measurement": "GB",
			"device_class":        "data_size",
			"retain":              true,
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"disk_free_gb": {
			"unique_id":           fmt.Sprintf("%s_disk_free_gb", DeviceIDConst),
			"name":                "Disk Free",
			"platform":            "sensor",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.disk_free_gb }}",
			"unit_of_measurement": "GB",
			"device_class":        "data_size",
			"retain":              true,
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"uptime_days": {
			"unique_id":           fmt.Sprintf("%s_uptime_days", DeviceIDConst),
			"name":                "Uptime Days",
			"platform":            "sensor",
			"state_topic":         stateTopic,
			"value_template":      "{{ value_json.uptime_days }}",
			"unit_of_measurement": "d",
			"device_class":        "duration",
			"retain":              true,
			"state_class":         "measurement",
			"entity_category":     "diagnostic",
		},
		"ip": {
			"unique_id":       fmt.Sprintf("%s_ip", DeviceIDConst),
			"name":            "IP Address",
			"platform":        "sensor",
			"state_topic":     stateTopic,
			"value_template":  "{{ value_json.ip }}",
			"retain":          true,
			"entity_category": "diagnostic",
		},
	}

	// Deterministic structs (device only has ids now)
	type discoveryDevice struct {
		IDs string `json:"ids"`
	}
	type discoveryRoot struct {
		Device              discoveryDevice                   `json:"device"`
		Origin              map[string]string                 `json:"origin"`
		Components          map[string]map[string]interface{} `json:"components"`
		StateTopic          string                            `json:"state_topic"`
		AvailabilityTopic   string                            `json:"availability_topic"`
		PayloadAvailable    string                            `json:"payload_available"`
		PayloadNotAvailable string                            `json:"payload_not_available"`
		QOS                 int                               `json:"qos"`
	}

	root := discoveryRoot{
		Device: discoveryDevice{
			IDs: deviceIdentifier,
		},
		Origin:              map[string]string{"name": "vanpix-rpi2mqtt", "sw_version": cfg.SWVersion, "url": "https://github.com/xsmod/vanpix-rpi2mqtt"},
		Components:          components,
		StateTopic:          stateTopic,
		AvailabilityTopic:   availabilityTopic,
		PayloadAvailable:    "online",
		PayloadNotAvailable: "offline",
		QOS:                 2,
	}
	b, _ := json.Marshal(root)
	// Always publish under fixed device id topic to ensure consistent discovery path
	configTopic := fmt.Sprintf("%s/device/%s/config", cfg.HAPrefix, DeviceIDConst)
	client.Publish(configTopic, 0, true, string(b)).Wait()
	log.Printf("published %s", configTopic)
}

// getCPUPercent computes CPU usage percent over the interval between calls using /proc/stat.
func getCPUPercent() float64 {
	idle, total := readCPUTimes()
	cpuMu.Lock()
	defer cpuMu.Unlock()
	if !prevCPUSet {
		prevCPUIdle, prevCPUTotal, prevCPUSet = idle, total, true
		return 0.0
	}
	deltaIdle := float64(idle - prevCPUIdle)
	deltaTotal := float64(total - prevCPUTotal)
	prevCPUIdle, prevCPUTotal = idle, total
	if deltaTotal <= 0 {
		return 0.0
	}
	usage := (1.0 - deltaIdle/deltaTotal) * 100.0
	if usage < 0 {
		usage = 0
	}
	if usage > 100 {
		usage = 100
	}
	return usage
}

// readCPUTimes reads the first line of /proc/stat and returns (idle, total) jiffies.
func readCPUTimes() (idle, total uint64) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	s := bufio.NewScanner(strings.NewReader(string(b)))
	for s.Scan() {
		line := s.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		var vals []uint64
		for _, f := range fields {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				v = 0
			}
			vals = append(vals, v)
		}
		// According to procfs, the first fields are:
		// user nice system idle iowait irq softirq steal guest guest_nice
		var user, nice, system, idleVal, iowait, irq, softirq, steal uint64
		if len(vals) > 0 {
			user = vals[0]
		}
		if len(vals) > 1 {
			nice = vals[1]
		}
		if len(vals) > 2 {
			system = vals[2]
		}
		if len(vals) > 3 {
			idleVal = vals[3]
		}
		if len(vals) > 4 {
			iowait = vals[4]
		}
		if len(vals) > 5 {
			irq = vals[5]
		}
		if len(vals) > 6 {
			softirq = vals[6]
		}
		if len(vals) > 7 {
			steal = vals[7]
		}
		total := user + nice + system + idleVal + iowait + irq + softirq + steal
		return idleVal + iowait, total
	}
	return 0, 0
}

// parseLoadAvgFromBytes remains for tests but is no longer used for CPU percent.
func parseLoadAvgFromBytes(b []byte) string {
	parts := strings.Fields(string(b))
	if len(parts) < 1 {
		return ""
	}
	return parts[0]
}

func getTemperature() float64 {
	// Try common thermal zone files
	paths := []string{"/sys/class/thermal/thermal_zone0/temp"}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			b, err := os.ReadFile(p)
			if err == nil {
				s := strings.TrimSpace(string(b))
				v, err := strconv.ParseFloat(s, 64)
				if err == nil {
					// many kernels report millidegrees
					if v > 1000 {
						return v / 1000.0
					}
					return v
				}
			}
		}
	}
	// Try vcgencmd fallback (not available in repo image)
	return 0.0
}

func getMemTotalMB() int64 {
	m := parseMemInfo()
	if v, ok := m["MemTotal"]; ok {
		return v / 1024
	}
	return 0
}

func getMemAvailableMB() int64 {
	m := parseMemInfo()
	if v, ok := m["MemAvailable"]; ok {
		return v / 1024
	}
	// Fallback to MemFree
	if v, ok := m["MemFree"]; ok {
		return v / 1024
	}
	return 0
}

func getMemFreeMB() int64 {
	m := parseMemInfo()
	if v, ok := m["MemFree"]; ok {
		return v / 1024
	}
	return 0
}

func parseMemInfo() map[string]int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil
	}
	return parseMemInfoFromBytes(b)
}

// parseMemInfoFromBytes parses the contents of /proc/meminfo into a map of values (kB).
func parseMemInfoFromBytes(b []byte) map[string]int64 {
	m := map[string]int64{}
	s := bufio.NewScanner(strings.NewReader(string(b)))
	for s.Scan() {
		line := s.Text()
		parts := strings.Split(line, ":")
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valStr := strings.TrimSpace(parts[1])
		valFields := strings.Fields(valStr)
		if len(valFields) == 0 {
			continue
		}
		v, err := strconv.ParseInt(valFields[0], 10, 64)
		if err == nil {
			m[key] = v
		}
	}
	return m
}

// parseUptimeFromBytes parses the first number from /proc/uptime and returns seconds.
func parseUptimeFromBytes(b []byte) int64 {
	parts := strings.Fields(string(b))
	if len(parts) < 1 {
		return 0
	}
	f, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0
	}
	return int64(f)
}

// parseUptimeSecondsFromBytes returns the uptime in seconds as float64 (keeps fractional seconds).
func parseUptimeSecondsFromBytes(b []byte) float64 {
	parts := strings.Fields(string(b))
	if len(parts) < 1 {
		return 0
	}
	f, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0
	}
	return f
}

// getUptimeDays reads /proc/uptime and returns the uptime in days as float64.
func getUptimeDays() float64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0.0
	}
	secs := parseUptimeSecondsFromBytes(b)
	return secs / 86400.0
}

func getDiskGB(path string) float64 {
	fs := syscall.Statfs_t{}
	_ = syscall.Statfs(path, &fs)
	total := float64(fs.Blocks) * float64(fs.Bsize)
	return total / (1024.0 * 1024.0 * 1024.0)
}

func getDiskFreeGB(path string) float64 {
	fs := syscall.Statfs_t{}
	_ = syscall.Statfs(path, &fs)
	free := float64(fs.Bavail) * float64(fs.Bsize)
	return free / (1024.0 * 1024.0 * 1024.0)
}

func getConfiguredIP() string {
	if v := os.Getenv("IP_ADDRESS"); v != "" {
		return v
	}
	if fp := os.Getenv("IP_FILE"); fp != "" {
		b, err := os.ReadFile(fp)
		if err == nil {
			ip := strings.TrimSpace(string(b))
			if ip != "" {
				return ip
			}
		}
	}
	return ""
}

// publish sends a retained MQTT message to the given topic.
// Behavior:
// - If disconnected, it performs a short, silent reconnect attempt.
// - On failure (timeout/error), it logs with the topic and reason.
// - On success, it logs a clear sentence including the topic.
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
	// Success: log a clear, human-friendly line including the topic
	log.Printf("published %s", topic)
}
