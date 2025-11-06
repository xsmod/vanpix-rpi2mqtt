package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
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

func env(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func main() {
	// Configuration from environment
	broker := env("MQTT_BROKER", "tcp://localhost:1883")
	user := env("MQTT_USER", "")
	pass := env("MQTT_PASSWORD", "")
	clientID := env("MQTT_CLIENT_ID", "rpi-stats")
	prefix := env("MQTT_TOPIC_PREFIX", "rpi-stats")
	intervalSec, _ := strconv.Atoi(env("INTERVAL_SECONDS", "30"))
	if intervalSec <= 0 {
		intervalSec = 30
	}
	deviceName := env("DEVICE_NAME", "rpi")
	haDiscovery := strings.ToLower(env("HA_DISCOVERY", "true")) != "false"
	haPrefix := env("HA_PREFIX", "homeassistant")
	// Path to use for disk calculations (allows mounting host root to /host and setting HOST_ROOT_PATH=/host)
	hostDiskPath = env("HOST_ROOT_PATH", "/")

	opts := mqtt.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(clientID)
	if user != "" {
		opts.Username = user
		opts.Password = pass
	}
	client := mqtt.NewClient(opts)

	// Retry connect with backoff
	maxAttempts := 10
	for i := 1; i <= maxAttempts; i++ {
		if token := client.Connect(); token.Wait() && token.Error() == nil {
			log.Println("connected to mqtt broker")
			break
		} else {
			log.Printf("mqtt connect attempt %d/%d failed: %v", i, maxAttempts, token.Error())
			if i == maxAttempts {
				log.Println("giving up connecting to mqtt broker, will continue and retry publishes")
				break
			}
			time.Sleep(time.Duration(i*2) * time.Second)
		}
	}

	deviceID := sanitize(deviceName)

	var wg sync.WaitGroup

	if haDiscovery {
		wg.Add(1)
		go func() {
			defer wg.Done()
			publishHADiscovery(client, haPrefix, prefix, deviceID, deviceName)
		}()
	}

	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	// Setup signal handling for graceful shutdown (Ctrl+C)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			// Run gather+publish in background so main goroutine remains responsive to signals
			wg.Add(1)
			go func() {
				defer wg.Done()
				report := gatherStats()
				publishMetrics(client, prefix, deviceID, report)
			}()
		case sig := <-sigCh:
			log.Printf("received signal %v, shutting down", sig)
			// Stop ticker and wait for in-flight publishes (with timeout)
			ticker.Stop()
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()
			select {
			case <-done:
				// all done
			case <-time.After(3 * time.Second):
				log.Println("timeout waiting for in-flight publishes")
			}
			if client != nil && client.IsConnected() {
				client.Disconnect(250) // wait up to 250ms for pending work
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
	CPULoad        string  `json:"cpu_load"`
	TemperatureC   float64 `json:"temperature_c"`
	TemperatureF   float64 `json:"temperature_f"`
	MemTotalMB     int64   `json:"mem_total_mb"`
	MemAvailableMB int64   `json:"mem_available_mb"`
	DiskTotalGB    float64 `json:"disk_total_gb"`
	DiskFreeGB     float64 `json:"disk_free_gb"`
	IP             string  `json:"ip"`
	UptimeDays     float64 `json:"uptime_days"`
}

func gatherStats() Stats {
	c := getTemperature()
	f := c*9.0/5.0 + 32.0
	return Stats{
		CPULoad:        getCPULoad(),
		TemperatureC:   c,
		TemperatureF:   f,
		MemTotalMB:     getMemTotalMB(),
		MemAvailableMB: getMemAvailableMB(),
		DiskTotalGB:    getDiskGB(hostDiskPath),
		DiskFreeGB:     getDiskFreeGB(hostDiskPath),
		IP:             getIP(),
		UptimeDays:     getUptimeDays(),
	}
}

func publishMetrics(client mqtt.Client, prefix, device string, s Stats) {
	top := func(metric string) string { return fmt.Sprintf("%s/%s/%s", prefix, device, metric) }

	publish(client, top("cpu_load"), s.CPULoad)
	publish(client, top("temperature_c"), fmt.Sprintf("%.2f", s.TemperatureC))
	publish(client, top("temperature_f"), fmt.Sprintf("%.2f", s.TemperatureF))
	publish(client, top("mem_total_mb"), fmt.Sprintf("%d", s.MemTotalMB))
	publish(client, top("mem_available_mb"), fmt.Sprintf("%d", s.MemAvailableMB))
	publish(client, top("disk_total_gb"), fmt.Sprintf("%.2f", s.DiskTotalGB))
	publish(client, top("disk_free_gb"), fmt.Sprintf("%.2f", s.DiskFreeGB))
	publish(client, top("ip"), s.IP)
	publish(client, top("uptime_days"), fmt.Sprintf("%.2f", s.UptimeDays))
}

func publish(client mqtt.Client, topic, payload string) {
	if client == nil {
		log.Printf("mqtt client nil, cannot publish %s", topic)
		return
	}
	if !client.IsConnected() {
		// try to reconnect non-blocking with timeout
		if token := client.Connect(); token != nil {
			if ok := token.WaitTimeout(2 * time.Second); !ok {
				log.Printf("publish: reconnect attempt timed out")
			} else if token.Error() != nil {
				log.Printf("publish: reconnect failed: %v", token.Error())
			}
		}
	}
	t := client.Publish(topic, 0, true, payload)
	// avoid waiting indefinitely
	if ok := t.WaitTimeout(5 * time.Second); !ok {
		log.Printf("publish %s timed out", topic)
		return
	}
	if t.Error() != nil {
		log.Printf("publish %s failed: %v", topic, t.Error())
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
		{"cpu_load", "CPU Load", "", ""},
		{"temperature_c", "Temperature (C)", "°C", "temperature"},
		{"temperature_f", "Temperature (F)", "°F", "temperature"},
		{"mem_total_mb", "Memory Total", "MB", "data_size"},
		{"mem_available_mb", "Memory Available", "MB", "data_size"},
		{"disk_total_gb", "Disk Total", "GB", "data_size"},
		{"disk_free_gb", "Disk Free", "GB", "data_size"},
		{"ip", "IP Address", "", ""},
		{"uptime_days", "Uptime", "d", ""},
	}

	for _, s := range sensors {
		objectID := fmt.Sprintf("%s_%s", deviceID, s.Metric)
		stateTopic := fmt.Sprintf("%s/%s/%s", prefix, deviceID, s.Metric)
		cfg := map[string]interface{}{
			"name":                fmt.Sprintf("%s %s", deviceName, s.Name),
			"state_topic":         stateTopic,
			"unique_id":           objectID,
			"unit_of_measurement": s.Unit,
			"device": map[string]interface{}{
				"identifiers":  []string{deviceID},
				"name":         deviceName,
				"model":        "Raspberry Pi",
				"manufacturer": "Raspberry Pi",
			},
		}

		// Add device_class and state_class for better HA integration
		if s.DeviceClass != "" {
			cfg["device_class"] = s.DeviceClass
		}
		if s.Metric != "ip" {
			cfg["state_class"] = "measurement"
		}

		b, _ := json.Marshal(cfg)
		topic := fmt.Sprintf("%s/sensor/%s/%s/config", haPrefix, deviceID, objectID)
		t := client.Publish(topic, 0, true, string(b))
		t.Wait()
		if t.Error() != nil {
			log.Printf("HA discovery publish failed: %v", t.Error())
		}
	}
}

func getCPULoad() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	return parseLoadAvgFromBytes(b)
}

// parseLoadAvgFromBytes extracts the 1-minute load average from /proc/loadavg content.
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

func getIP() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifs {
		if (iface.Flags & net.FlagUp) == 0 {
			continue
		}
		if (iface.Flags & net.FlagLoopback) != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			switch v := a.(type) {
			case *net.IPNet:
				ip := v.IP
				if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
					continue
				}
				if ip.To4() != nil {
					return ip.String()
				}
			case *net.IPAddr:
				ip := v.IP
				if ip.To4() != nil && !ip.IsLoopback() {
					return ip.String()
				}
			}
		}
	}
	return ""
}
