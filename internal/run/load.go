package run

import (
	"os"
	"strconv"
	"strings"
)

// LoadFactor sets the default machine-load limit for checks (checks.load_limit, T-720): LoadFactor × NumCPU. A
// 1-minute load average above it means the machine is overloaded, so a check would race for CPU and could go red
// from load alone (T-714's gate: load 50-88 on 10 cores).
const LoadFactor = 2

// SystemLoad is the machine's 1-minute load average, read in-process (sysctl vm.loadavg on macOS, else
// /proc/loadavg); ok false when it cannot be read.
func SystemLoad() (float64, bool) {
	if l, ok := sysctlLoad(); ok {
		return l, true
	}
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	return parseLoadavg(string(b))
}

// parseLoadavg reads the first field of /proc/loadavg's text.
func parseLoadavg(text string) (float64, bool) {
	f := strings.Fields(text)
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil && v >= 0
}

// UnderLoad is the mark of a check that claimed its slot under load (Acquire's loaded): " (run under load <x>)",
// "" for 0.
func UnderLoad(loaded float64) string {
	if loaded <= 0 {
		return ""
	}
	return " (run under load " + FormatLoad(loaded) + ")"
}

// FormatLoad is a load average with two decimals at most ("45.1", "20").
func FormatLoad(f float64) string {
	return strconv.FormatFloat(float64(int64(f*100+0.5))/100, 'f', -1, 64)
}
