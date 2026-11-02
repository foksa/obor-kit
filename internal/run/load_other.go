//go:build !darwin

package run

// sysctlLoad: no sysctl vm.loadavg here (SystemLoad reads /proc/loadavg).
func sysctlLoad() (float64, bool) { return 0, false }
