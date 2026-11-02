package run

import (
	"encoding/binary"
	"syscall"
)

// sysctlLoad decodes sysctl vm.loadavg, struct loadavg { fixpt_t ldavg[3]; long fscale; }: three uint32, padding,
// an int64; the load is ldavg[0] / fscale. syscall.Sysctl drops one trailing NUL byte (fscale's high byte).
func sysctlLoad() (float64, bool) {
	s, err := syscall.Sysctl("vm.loadavg")
	if err != nil || len(s) < 16 {
		return 0, false
	}
	b := []byte(s)
	for len(b) < 24 {
		b = append(b, 0)
	}
	scale := binary.NativeEndian.Uint64(b[16:24])
	if scale == 0 {
		return 0, false
	}
	return float64(binary.NativeEndian.Uint32(b[0:4])) / float64(scale), true
}
