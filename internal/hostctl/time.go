package hostctl

import (
	"context"
	"strings"
	"time"

	"freewaypi/internal/i18n"
	"freewaypi/internal/privhelper"
)

// Time is the box's own clock settings.
//
// Not the ventilation unit's clock — this is what that one gets set from. A box
// left on the image's default timezone writes an hour's error into the unit and
// every timer programme that follows it, and nothing on the page says so,
// because the browser shows times in the reader's own zone. The error is
// invisible exactly where somebody would look for it.
type Time struct {
	Zone string `json:"zone"`
	// NTP is whether time synchronisation is switched on, Synchronised whether
	// it has actually succeeded. The second is the one worth reading: a server
	// nobody can resolve looks identical to a working one until it is asked.
	NTP          bool      `json:"ntp"`
	Synchronised bool      `json:"synchronised"`
	Now          time.Time `json:"now"`
	// Servers is what has been configured, empty for the distribution's pool.
	Servers []string `json:"servers"`
	// ServerNow is the one actually in use, which differs from Servers when a
	// change has not taken or a name does not resolve.
	ServerNow string `json:"server_now,omitempty"`

	Writable bool   `json:"writable"`
	Why      string `json:"why,omitempty"`
}

// ReadTime asks the helper.
func ReadTime(ctx context.Context) *Time {
	t := &Time{}
	out, err := privhelper.Call(ctx, "time-status")
	if err != nil {
		t.Why = err.Error()
		return t
	}
	t.Writable = true
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "timezone":
			t.Zone = value
		case "ntp":
			t.NTP = value == "yes"
		case "synchronised":
			t.Synchronised = value == "yes"
		case "now":
			if parsed, err := time.Parse(time.RFC3339, value); err == nil {
				t.Now = parsed
			}
		case "servers":
			t.Servers = strings.Fields(value)
		case "server_now":
			t.ServerNow = value
		}
	}
	return t
}

// SetZone changes the timezone. The name is checked against the zoneinfo
// database by the helper, because "Europe/Nowhere" has the right shape.
//
// It reports whether the daemon is about to restart: it has to, to see a new
// zone at all, and the page needs to know so it can wait and reload rather
// than show an error when the connection drops.
func SetZone(ctx context.Context, zone string) (restarting bool, err error) {
	if strings.TrimSpace(zone) == "" {
		return false, i18n.Errf("error.time.zone")
	}
	out, err := privhelper.Call(ctx, "time-zone", privhelper.Field{Key: "zone", Value: zone})
	return strings.TrimSpace(out) == "restarting", err
}

// SetNTP sets which time servers to use. Empty goes back to the distribution's
// own pool, which is the right default and worth being able to return to.
func SetNTP(ctx context.Context, servers []string) error {
	_, err := privhelper.Call(ctx, "time-ntp",
		privhelper.Field{Key: "servers", Value: strings.Join(servers, " ")})
	return err
}
