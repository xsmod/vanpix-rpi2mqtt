package main

import (
	"bufio"
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

var hostDiskPath string

// Protect previous CPU counters for percent calculation across intervals
var cpuMu sync.Mutex
var prevCPUTotal uint64
var prevCPUIdle uint64
var prevCPUSet bool

func env(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

type AppConfig struct {
	Broker       string
	User         string
	Password     string
	ClientID     string
	Prefix       string
	IntervalSec  int
	DeviceName   string
	HADiscovery  bool
	HAPrefix     string
	HostDiskPath string
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
	return AppConfig{
		Broker:       env("MQTT_BROKER", "tcp://localhost:1883"),
		User:         user,
		Password:     pass,
		ClientID:     env("MQTT_CLIENT_ID", "rpi-stats"),
		Prefix:       env("MQTT_TOPIC_PREFIX", "rpi-stats"),
		IntervalSec:  intervalSec,
		DeviceName:   env("DEVICE_NAME", "rpi"),
		HADiscovery:  strings.ToLower(env("HA_DISCOVERY", "true")) != "false",
		HAPrefix:     env("HA_PREFIX", "homeassistant"),
		HostDiskPath: env("HOST_ROOT_PATH", "/"),
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

func main() {
	cfg := loadConfig()
	hostDiskPath = cfg.HostDiskPath

	opts := applyConfigToOptions(cfg)
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

	deviceID := sanitize(cfg.DeviceName)

	var wg sync.WaitGroup

	if cfg.HADiscovery {
		wg.Add(1)
		go func() {
			defer wg.Done()
			publishHADiscovery(client, cfg.HAPrefix, cfg.Prefix, deviceID, cfg.DeviceName)
		}()
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
				publishMetrics(client, cfg.Prefix, deviceID, report)
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
			if client != nil && client.IsConnected() {
				client.Disconnect(250)
			}
			return
		}
	}
}

func sanitize(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "_")
	return s
}

type Stats struct {
	CPULoad        float64 `json:"cpu_load"` // percent 0-100
	TemperatureC   float64 `json:"temperature_c"`
	TemperatureF   float64 `json:"temperature_f"`
	MemTotalMB     int64   `json:"mem_total_mb"`
	MemAvailableMB int64   `json:"mem_available_mb"`
	MemFreeMB      int64   `json:"mem_free_mb"`
	DiskTotalGB    float64 `json:"disk_total_gb"`
	DiskFreeGB     float64 `json:"disk_free_gb"`
	UptimeDays     float64 `json:"uptime_days"`
	IP             string  `json:"ip"`
}

func gatherStats() Stats {
	c := getTemperature()
	f := c*9.0/5.0 + 32.0
	return Stats{
		CPULoad:        getCPUPercent(),
		TemperatureC:   c,
		TemperatureF:   f,
		MemTotalMB:     getMemTotalMB(),
		MemAvailableMB: getMemAvailableMB(),
		MemFreeMB:      getMemFreeMB(),
		DiskTotalGB:    getDiskGB(hostDiskPath),
		DiskFreeGB:     getDiskFreeGB(hostDiskPath),
		UptimeDays:     getUptimeDays(),
		IP:             getConfiguredIP(),
	}
}

// publishMetrics publishes all metrics for a single Stats snapshot under the configured prefix/device.
func publishMetrics(client mqtt.Client, prefix, device string, s Stats) {
	top := func(metric string) string { return fmt.Sprintf("%s/%s/%s", prefix, device, metric) }

	publish(client, top("cpu_load"), fmt.Sprintf("%.1f", s.CPULoad))
	publish(client, top("temperature_c"), fmt.Sprintf("%.2f", s.TemperatureC))
	publish(client, top("temperature_f"), fmt.Sprintf("%.2f", s.TemperatureF))
	publish(client, top("mem_total_mb"), fmt.Sprintf("%d", s.MemTotalMB))
	publish(client, top("mem_available_mb"), fmt.Sprintf("%d", s.MemAvailableMB))
	publish(client, top("mem_free_mb"), fmt.Sprintf("%d", s.MemFreeMB))
	publish(client, top("disk_total_gb"), fmt.Sprintf("%.2f", s.DiskTotalGB))
	publish(client, top("disk_free_gb"), fmt.Sprintf("%.2f", s.DiskFreeGB))
	publish(client, top("uptime_days"), fmt.Sprintf("%.2f", s.UptimeDays))
	if s.IP != "" {
		publish(client, top("ip"), s.IP)
	}
}

func publishHADiscovery(client mqtt.Client, haPrefix, prefix, deviceID, deviceName string) {
	type sensorDef struct {
		Metric      string
		Name        string
		Unit        string
		DeviceClass string
	}

	sensors := []sensorDef{
		{"cpu_load", "CPU Load", "%", ""},
		{"temperature_c", "Temperature (C)", "°C", "temperature"},
		{"temperature_f", "Temperature (F)", "°F", "temperature"},
		{"mem_total_mb", "Memory Total", "MB", "data_size"},
		{"mem_available_mb", "Memory Available", "MB", "data_size"},
		{"mem_free_mb", "Memory Free", "MB", "data_size"},
		{"disk_total_gb", "Disk Total", "GB", "data_size"},
		{"disk_free_gb", "Disk Free", "GB", "data_size"},
		{"uptime_days", "Uptime", "d", "duration"},
	}
	if ip := getConfiguredIP(); ip != "" {
		// add IP sensor non-numeric (no unit, no device_class/state_class)
		sensors = append(sensors, sensorDef{"ip", "IP Address", "", ""})
	}

	for _, s := range sensors {
		objectID := fmt.Sprintf("%s_%s", deviceID, s.Metric)
		stateTopic := fmt.Sprintf("%s/%s/%s", prefix, deviceID, s.Metric)
		cfg := map[string]interface{}{
			"name":        fmt.Sprintf("%s %s", deviceName, s.Name),
			"state_topic": stateTopic,
			"unique_id":   objectID,
			"device": map[string]interface{}{
				"identifiers":  []string{deviceID},
				"name":         deviceName,
				"model":        "Raspberry Pi",
				"manufacturer": "Raspberry Pi",
			},
		}
		if s.Unit != "" {
			cfg["unit_of_measurement"] = s.Unit
		}
		if s.DeviceClass != "" {
			cfg["device_class"] = s.DeviceClass
		} else if isDataSizeUnit(s.Unit) {
			cfg["device_class"] = "data_size"
		}
		if s.Metric != "ip" {
			cfg["state_class"] = "measurement"
		}
		if s.Metric == "uptime_days" {
			cfg["platform"] = "sensor"
		}

		b, _ := json.Marshal(cfg)
		topic := fmt.Sprintf("%s/sensor/%s/%s/config", haPrefix, deviceID, objectID)
		client.Publish(topic, 0, true, string(b)).Wait()
	}
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

func isDataSizeUnit(u string) bool {
	switch u {
	case "B", "kB", "MB", "GB", "TB", "PB", "KiB", "MiB", "GiB", "TiB", "PiB":
		return true
	default:
		return false
	}
}
