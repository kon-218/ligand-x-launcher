// Package hostmetrics reads host CPU, memory, network, disk and NVIDIA GPU
// usage for the launcher dashboard and pre-flight checks. The /proc readers
// return zero values on platforms without procfs.
package hostmetrics

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func CPU() (float64, string) {
	idle1, total1, ok1 := readProcStat()
	time.Sleep(120 * time.Millisecond)
	idle2, total2, ok2 := readProcStat()
	load := "-"
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			load = strings.Join(fields[:3], " ")
		}
	}
	if !ok1 || !ok2 || total2 <= total1 {
		return 0, load
	}
	idleDelta := float64(idle2 - idle1)
	totalDelta := float64(total2 - total1)
	return (1 - idleDelta/totalDelta) * 100, load
}

func readProcStat() (uint64, uint64, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line := strings.SplitN(string(data), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var total uint64
	var values []uint64
	for _, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		values = append(values, v)
		total += v
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return idle, total, true
}

func Memory() (uint64, uint64, float64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		values[key] = v * 1024
	}
	total := values["MemTotal"]
	available := values["MemAvailable"]
	if total == 0 || available > total {
		return 0, total, 0
	}
	used := total - available
	return used, total, float64(used) / float64(total) * 100
}

func Network() (uint64, uint64) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	var rx, tx uint64
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		iface := strings.TrimSpace(parts[0])
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 16 {
			continue
		}
		r, _ := strconv.ParseUint(fields[0], 10, 64)
		t, _ := strconv.ParseUint(fields[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx
}

func DiskUsage(path string) (uint64, uint64) {
	if runtime.GOOS == "windows" {
		return 0, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "df", "-k", path).Output()
	if err != nil {
		return 0, 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, 0
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0, 0
	}
	totalKB, _ := strconv.ParseUint(fields[1], 10, 64)
	usedKB, _ := strconv.ParseUint(fields[2], 10, 64)
	return usedKB * 1024, totalKB * 1024
}

func NvidiaGPU() (float64, uint64, uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=utilization.gpu,memory.used,memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0, 0, 0
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	parts := strings.Split(line, ",")
	if len(parts) < 3 {
		return 0, 0, 0
	}
	util, _ := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	used, _ := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
	total, _ := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64)
	return util, used, total
}

func FormatBytes(n uint64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
