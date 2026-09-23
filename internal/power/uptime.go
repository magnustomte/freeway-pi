package power

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func parseUint(s string) (uint64, error) {
	return strconv.ParseUint(strings.TrimSpace(s), 10, 64)
}

// uptime is how long the box has been up, which is what turns a kernel
// timestamp into a time of day.
func uptime() (time.Duration, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	secs, err := strconv.ParseFloat(first, 64)
	if err != nil {
		return 0, fmt.Errorf("power: /proc/uptime: %w", err)
	}
	return time.Duration(secs * float64(time.Second)), nil
}
