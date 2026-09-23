package hostctl

import (
	"context"
	"strings"

	"freewaypi/internal/privhelper"
)

// PowerLog returns the kernel's own words about the supply, oldest first.
//
// Through the helper because the daemon runs with ProtectKernelLogs=yes and
// cannot read /dev/kmsg itself — which is deliberate, and stays. The helper
// returns only the lines that mention voltage, so the rest of the kernel log,
// addresses and all, never reaches a process listening on the network.
func PowerLog(ctx context.Context) ([]string, error) {
	out, err := privhelper.Call(ctx, "power-log")
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "line="); ok {
			lines = append(lines, value)
		}
	}
	return lines, nil
}
