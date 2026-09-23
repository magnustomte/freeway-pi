package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultIsValidAndRefusesEveryClient(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("the default configuration is invalid: %v", err)
	}
	if c.Gateway.Enabled {
		t.Fatal("the gateway is enabled by default; it must be turned on deliberately")
	}
	if len(c.Gateway.Allow) != 0 {
		t.Fatal("the default configuration lists allowed peers")
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing file should not be an error: %v", err)
	}
	if c.Serial.Port != Default().Serial.Port {
		t.Fatal("defaults were not applied")
	}
}

func TestLoadFillsInFieldsAnOlderFileLacks(t *testing.T) {
	// A file written by an earlier version must not leave a new timeout at
	// zero, which would mean giving up before the unit could answer.
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"serial":{"port":"/dev/ttyUSB0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Serial.Port != "/dev/ttyUSB0" {
		t.Errorf("port = %q, want the value from the file", c.Serial.Port)
	}
	if c.Serial.Timeout.Std() != Default().Serial.Timeout.Std() {
		t.Errorf("timeout = %v, want the default", c.Serial.Timeout.Std())
	}
	if c.Serial.Baud != Default().Serial.Baud {
		t.Errorf("baud = %d, want the default", c.Serial.Baud)
	}
	if c.Poll.Interval.Std() != Default().Poll.Interval.Std() {
		t.Errorf("poll interval = %v, want the default", c.Poll.Interval.Std())
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	in := Default()
	in.Gateway.Enabled = true
	in.Gateway.Allow = []string{"192.0.2.0/24"}
	in.Poll.Interval = Duration(30 * time.Second)

	if err := in.Save(path); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Gateway.Enabled || len(out.Gateway.Allow) != 1 || out.Gateway.Allow[0] != "192.0.2.0/24" {
		t.Fatalf("gateway did not survive the round trip: %+v", out.Gateway)
	}
	if out.Poll.Interval.Std() != 30*time.Second {
		t.Fatalf("poll interval = %v, want 30s", out.Poll.Interval.Std())
	}
}

func TestDurationsAreWrittenAsReadableText(t *testing.T) {
	// A bare number of nanoseconds in a file someone may edit is a trap.
	data, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"interval":"10s"`) {
		t.Fatalf("poll interval was not written as text: %s", data)
	}
}

func TestDurationAcceptsTextAndPlainSeconds(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte(`"1m30s"`), &d); err != nil {
		t.Fatal(err)
	}
	if d.Std() != 90*time.Second {
		t.Errorf("parsed %v, want 90s", d.Std())
	}
	if err := json.Unmarshal([]byte(`15`), &d); err != nil {
		t.Fatal(err)
	}
	if d.Std() != 15*time.Second {
		t.Errorf("a bare 15 parsed to %v, want 15s", d.Std())
	}
	if err := json.Unmarshal([]byte(`"soon"`), &d); err == nil {
		t.Error("accepted a duration of \"soon\"")
	}
}

func TestValidateCatchesConfigurationThatCannotWork(t *testing.T) {
	cases := map[string]func(*Config){
		"empty port":            func(c *Config) { c.Serial.Port = "" },
		"zero baud":             func(c *Config) { c.Serial.Baud = 0 },
		"broadcast unit id":     func(c *Config) { c.Serial.UnitID = 0 },
		"zero poll interval":    func(c *Config) { c.Poll.Interval = 0 },
		"enabled but no listen": func(c *Config) { c.Gateway.Enabled = true; c.Gateway.Listen = "" },
	}
	for name, break_ := range cases {
		c := Default()
		break_(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestSaveLeavesNoPartialFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := Default().Save(path); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d files in the directory, want only the config", len(entries))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("mode %o, want 640; this file will hold credentials", perm)
	}
}

func TestLoadRejectsBrokenJSONRatherThanStartingWrong(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"serial":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a truncated file was accepted")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
