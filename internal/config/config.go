// Package config loads and saves freewayd's configuration.
//
// The format is JSON, not because it is pleasant to edit by hand but because
// reading it needs nothing outside the standard library. This runs on a box
// nobody maintains, and every dependency is something that will eventually want
// patching.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultPath is where the installer puts the file.
const DefaultPath = "/etc/freeway/config.json"

// Config is the whole configuration.
type Config struct {
	Serial  Serial  `json:"serial"`
	Poll    Poll    `json:"poll"`
	Unit    Unit    `json:"unit"`
	History History `json:"history"`
	Notify  Notify  `json:"notify"`
	Updates Updates `json:"updates"`
	Auth    Auth    `json:"auth"`
	Web     Web     `json:"web"`
	Gateway Gateway `json:"gateway"`
	// LogLevel is debug, info, warn or error.
	LogLevel string `json:"log_level"`
}

// Serial describes the RS-485 link.
type Serial struct {
	// Port should be the stable name the udev rule creates, not ttyUSBn,
	// which moves with USB enumeration order.
	Port string `json:"port"`
	Baud int    `json:"baud"`
	// UnitID is the address the background poller talks to. Requests arriving
	// over Modbus TCP carry their own, which is forwarded as given.
	UnitID byte `json:"unit_id"`
	// Timeout bounds one attempt on the wire.
	Timeout Duration `json:"timeout"`
	// Retries is the number of extra attempts after a transport failure.
	// Measurement on a live unit found a raw failure rate of a few per
	// thousand, random and unrelated to how fast requests are sent, so
	// retrying is what keeps them invisible.
	Retries int `json:"retries"`
	// InterFrame is silence held between requests. Measurement found no
	// relationship between this and the failure rate at any value from zero to
	// a hundred milliseconds, so a small value is politeness, not necessity.
	InterFrame Duration `json:"inter_frame"`
}

// Poll describes the background refresh.
type Poll struct {
	// Interval defaults to the unit's own measurement refresh interval, which
	// it reports in holding register 726. Polling faster returns the same
	// values rather than fresher ones.
	Interval Duration `json:"interval"`
}

// Unit describes how freewayd looks after the ventilation unit itself.
type Unit struct {
	// Name is what the interface calls this unit. Empty means "whatever it
	// turns out to be": HR597 holds a code for the model, so a unit that has
	// never been named shows what it is — a Pelican, a Pegasos — rather than
	// the word "Aggregat". Somebody with two of them can still name them.
	//
	// That register was found by reading the original adapter's Info tab. The
	// earlier note here, that nothing on the unit carries a model, was wrong.
	Name string `json:"name"`
	// SyncClock keeps the unit's real-time clock in step with system time.
	//
	// It drives the unit's week and year programs, and it drifts: a unit that
	// has never been adjusted for summer time can be hours out. The controller
	// has no way to know about NTP, so the replacement does it.
	SyncClock bool `json:"sync_clock"`
	// SyncClockInterval is how often to check. The check is one comparison
	// against the snapshot already in hand and writes only when it has drifted.
	SyncClockInterval Duration `json:"sync_clock_interval"`
	// SyncClockTolerance is how far the unit may drift before it is corrected.
	// Writing on every small difference would put pointless traffic on the bus.
	SyncClockTolerance Duration `json:"sync_clock_tolerance"`
}

// Auth holds the credentials for the parts beyond daily use. It is written by
// the interface when a PIN is first set, not edited by hand.
type Auth struct {
	Salt          string `json:"salt"`
	Verifier      string `json:"verifier"`
	Iterations    int    `json:"iterations"`
	SessionSecret string `json:"session_secret"`
	SessionDays   int    `json:"session_days"`
}

// History describes the measurement history.
type History struct {
	Enabled bool `json:"enabled"`
	// Path is the database file. Under the systemd StateDirectory, so a
	// package upgrade never goes near it.
	Path string `json:"path"`
	// Interval is how often to look for a new snapshot to store. Half the poll
	// interval, so no snapshot is missed, and duplicates are dropped anyway.
	Interval Duration `json:"interval"`
	// Housekeeping is how often to roll up and prune.
	Housekeeping Duration `json:"housekeeping"`
}

// Notify describes where word is sent when something happens.
// Updates is what to do about an update that wants a reboot.
//
// Security updates install themselves, so a box can end up needing a reboot
// without anybody asking for one — and then sit there for weeks because nobody
// happened to open the System page. Freeway Pi will not restart a ventilation
// unit on its own initiative, so this is the one place that decision is made,
// and it is made by whoever owns the house.
type Updates struct {
	// RebootPolicy is "notify" or "auto". Empty means notify, because a
	// ventilation unit that disappears at three in the morning because a
	// package was updated is not behaviour anybody asked for.
	RebootPolicy string `json:"reboot_policy"`
	// RebootAt is when to do it under "auto", as HH:MM in the box's own
	// timezone. Empty means 04:00.
	RebootAt string `json:"reboot_at"`
}

type Notify struct {
	// MinSeverity is info, warning or critical. Resolutions ignore it: an
	// alert with no all-clear leaves somebody believing a problem is running.
	MinSeverity string `json:"min_severity"`
	// DownAfter is how long the unit may be unreachable before it is worth
	// waking somebody. One missed pass is a hiccup.
	DownAfter Duration  `json:"down_after"`
	Interval  Duration  `json:"interval"`
	Webhooks  []Webhook `json:"webhooks"`
	SMTP      SMTP      `json:"smtp"`
	// Language is what notifications are written in: the language the
	// interface was in when the notification settings were last saved. A
	// message sent by webhook or mail has no reader with a browser to ask, so
	// it takes the language of the person who set it up. Empty is the
	// interface's default.
	Language string `json:"language,omitempty"`
}

// Webhook is one place to POST events.
type Webhook struct {
	Enabled bool              `json:"enabled"`
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// SMTP is mail delivery. Offered because the old adapter had it; a webhook is
// the sturdier choice, needing no credentials on the box.
type SMTP struct {
	Enabled  bool     `json:"enabled"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	STARTTLS bool     `json:"starttls"`
}

// Web describes the user interface.
type Web struct {
	Enabled bool `json:"enabled"`
	// Listen is the address the interface binds. Port 80 by default: this is a
	// self-contained appliance and nothing else runs on it, so there is no
	// reason to make anyone remember a port number.
	Listen string `json:"listen"`
	// ReadHeaderTimeout guards against a connection that opens and then says
	// nothing, which is the cheapest way to tie up a server.
	ReadHeaderTimeout Duration `json:"read_header_timeout"`
}

// Gateway describes the Modbus TCP service.
type Gateway struct {
	// Enabled is false by default. The gateway is something you turn on
	// deliberately, and turning it on is where you say who may use it.
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"`
	// Allow holds addresses and CIDR ranges. Ranges matter: a client on DHCP
	// should not lose access because its lease moved.
	Allow []string `json:"allow"`
	// RequestTimeout bounds one client request. It sits below the five seconds
	// the Homey app allows, so a client times out because the unit is slow
	// rather than because this gateway held on too long.
	RequestTimeout Duration `json:"request_timeout"`
	// BreakerThreshold is how many consecutive failures stop the gateway
	// putting requests on the bus at all.
	BreakerThreshold int `json:"breaker_threshold"`
	// BreakerCooldown is how long to wait before testing whether the unit is
	// back.
	BreakerCooldown Duration `json:"breaker_cooldown"`
}

// Default returns a configuration that is safe to start with: it talks to the
// unit and refuses every Modbus client, so nothing is exposed before someone
// has said who should reach it.
func Default() Config {
	return Config{
		Serial: Serial{
			Port:       "/dev/freeway-rs485",
			Baud:       19200,
			UnitID:     1,
			Timeout:    Duration(time.Second),
			Retries:    2,
			InterFrame: Duration(5 * time.Millisecond),
		},
		Poll: Poll{Interval: Duration(10 * time.Second)},
		Unit: Unit{
			// Empty: let the unit say what it is.
			Name: "",
			// Off by default: it writes to the unit, and nothing should start
			// writing to someone's ventilation because they installed an
			// update. The interface offers it where the drift is reported.
			SyncClock:          false,
			SyncClockInterval:  Duration(6 * time.Hour),
			SyncClockTolerance: Duration(2 * time.Minute),
		},
		History: History{
			Enabled:      true,
			Path:         "/var/lib/freeway/history.db",
			Interval:     Duration(5 * time.Second),
			Housekeeping: Duration(time.Minute),
		},
		Notify: Notify{
			MinSeverity: "warning",
			DownAfter:   Duration(5 * time.Minute),
			Interval:    Duration(30 * time.Second),
			SMTP:        SMTP{Port: 587, STARTTLS: true},
		},
		Auth: Auth{SessionDays: 90},
		Web: Web{
			Enabled:           true,
			Listen:            ":80",
			ReadHeaderTimeout: Duration(10 * time.Second),
		},
		Gateway: Gateway{
			Enabled:          false,
			Listen:           ":502",
			Allow:            nil,
			RequestTimeout:   Duration(4 * time.Second),
			BreakerThreshold: 3,
			BreakerCooldown:  Duration(5 * time.Second),
		},
		LogLevel: "info",
	}
}

// Load reads the file at path. A missing file is not an error: the defaults
// are used and written back, so a first start produces a file to edit.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}
	// Unmarshalling onto the defaults means an old file missing a new field
	// gets that field's default rather than its zero value, which for a
	// timeout would mean "give up immediately".
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, cfg.Validate()
}

// Validate reports configuration that would not work.
func (c Config) Validate() error {
	if c.Serial.Port == "" {
		return fmt.Errorf("config: serial.port is empty")
	}
	if c.Serial.Baud <= 0 {
		return fmt.Errorf("config: serial.baud is %d", c.Serial.Baud)
	}
	if c.Serial.UnitID == 0 {
		return fmt.Errorf("config: serial.unit_id is 0; Modbus reserves that for broadcast")
	}
	if c.Poll.Interval <= 0 {
		return fmt.Errorf("config: poll.interval is %v", time.Duration(c.Poll.Interval))
	}
	if c.History.Enabled && c.History.Path == "" {
		return fmt.Errorf("config: history is enabled but history.path is empty")
	}
	if c.Web.Enabled && c.Web.Listen == "" {
		return fmt.Errorf("config: the web interface is enabled but web.listen is empty")
	}
	if c.Gateway.Enabled && c.Gateway.Listen == "" {
		return fmt.Errorf("config: gateway is enabled but gateway.listen is empty")
	}
	return nil
}

// Save writes the configuration, replacing the file atomically so a crash
// partway through cannot leave the daemon unable to start next time.
func (c Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("config: temporary file in %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Duration is a time.Duration that reads and writes as "10s" in JSON, because
// a bare number of nanoseconds in a file someone may edit is a trap.
type Duration time.Duration

// MarshalJSON renders the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts "10s" and, for tolerance, a plain number of seconds.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("config: %q is not a duration such as \"10s\": %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	var n float64
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("config: expected a duration such as \"10s\"")
	}
	*d = Duration(time.Duration(n * float64(time.Second)))
	return nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }
