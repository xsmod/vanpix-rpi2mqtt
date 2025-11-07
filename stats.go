package main

import (
	"bufio"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Protect previous CPU counters for percent calculation across intervals
var cpuMu sync.Mutex
var prevCPUTotal uint64
var prevCPUIdle uint64
var prevCPUSet bool

// Stats is the aggregated JSON shape published to the state topic.
type Stats struct {
	CPULoad        float64 `json:"cpu_load"` // percent 0-100
	Temperature    int     `json:"temperature"`
	MemTotalMB     int64   `json:"mem_total_mb"`
	MemAvailableMB int64   `json:"mem_available_mb"`
	MemFreeMB      int64   `json:"mem_free_mb"`
	DiskTotalGB    int64   `json:"disk_total_gb"`
	DiskFreeGB     int64   `json:"disk_free_gb"`
	UptimeDays     float64 `json:"uptime_days"`
}

// gatherStats collects host stats and formats them for JSON publishing.
func gatherStats() Stats {
	c := int(math.Round(getTemperature()))
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

// getTemperature reads common kernel thermal zone files and returns temperature in °C.
func getTemperature() float64 {
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

// parseLoadAvgFromBytes remains for tests but is no longer used for CPU percent.
func parseLoadAvgFromBytes(b []byte) string {
	parts := strings.Fields(string(b))
	if len(parts) < 1 {
		return ""
	}
	return parts[0]
}
