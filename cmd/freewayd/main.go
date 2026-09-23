// SPDX-License-Identifier: GPL-3.0-or-later
//
// Copyright (C) 2026 Magnus Fonn Tømte
//
// This file is part of the Freeway Pi daemon. It comes with no warranty
// whatsoever; see the GNU General Public License in LICENSE for the terms it
// is offered under.

// Command freewayd replaces the Freeway WEB bus adapter.
//
// It owns the RS-485 port, serves Modbus TCP to clients that used to talk to
// Freeway, and keeps a recent copy of the unit's whole register map for the web
// interface to serve from.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"freewaypi/internal/hostctl"
	"freewaypi/internal/i18n"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"freewaypi/internal/auth"
	"freewaypi/internal/bus"
	"freewaypi/internal/config"
	"freewaypi/internal/control"
	"freewaypi/internal/gateway"
	"freewaypi/internal/modbus"
	"freewaypi/internal/netconf"
	"freewaypi/internal/notify"
	"freewaypi/internal/power"
	"freewaypi/internal/serial"
	"freewaypi/internal/store"
	"freewaypi/internal/system"
	"freewaypi/internal/web"
)

// version, commit and built are set at build time with -ldflags "-X main.…".
//
// Kept apart so the interface can show the release name and keep the commit as
// fine print: on an untagged repository a combined string is a bare hash, which
// reads as noise to everyone but the person who built it.
var (
	version = "dev"
	commit  = ""
	built   = ""
)

func main() {
	var (
		configPath = flag.String("config", config.DefaultPath, "configuration file")
		writeConf  = flag.Bool("write-config", false, "write the configuration back, filling in defaults, and exit")
		showVer    = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(versionString())
		return
	}

	if err := run(*configPath, *writeConf); err != nil {
		slog.Error("freewayd stopped", "err", err)
		os.Exit(1)
	}
}

func run(configPath string, writeConf bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if writeConf {
		return cfg.Save(configPath)
	}

	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)
	log.Info("freewayd starting", "version", versionString(), "config", configPath)
	// In the background: the count takes about fifteen seconds, and nothing
	// else should wait on it. It also warms the cache, so the system page has
	// a real figure by the time anybody opens it.
	go logUpdateState(log)
	go logNetwork(log)

	// A missing adapter is not a reason to refuse to start.
	//
	// On a freshly flashed box nothing is plugged in yet, and a daemon that
	// exits takes the web interface with it — so the page that would say "no
	// adapter found" is the one that will not load, and the box looks broken
	// rather than unfinished. Losing the bus while running is already handled
	// everywhere: the reopen loop, exception 11 to Modbus clients, and the
	// strip across the top of every page. This is that state, on purpose.
	port, err := serial.Open(serialConfig(cfg))
	if err != nil {
		log.Error("could not open the serial port; starting anyway so the interface answers",
			"port", cfg.Serial.Port, "err", err)
		port = serial.Closed(serialConfig(cfg))
	}
	defer port.Close()

	client := modbus.NewClient(port, cfg.Serial.UnitID)
	client.Timeout = cfg.Serial.Timeout.Std()
	client.Retries = cfg.Serial.Retries
	client.InterFrame = cfg.Serial.InterFrame.Std()

	b := bus.New(client, bus.Options{
		// So an adapter that was unplugged and put back comes good on its own.
		Reopen: port.Reopen,
		Logger: log,
	})
	defer b.Close()

	// Prove the link before announcing anything. The clock block is
	// unambiguous, so a plausible answer means the wiring, the baud rate and
	// the unit address are all right.
	if err := reportUnit(b, log); err != nil {
		// Not fatal: the unit may be powered down, and the daemon should come
		// back on its own when it returns rather than needing a restart.
		log.Warn("the unit did not answer at startup", "err", err)
	}

	poller := bus.NewPoller(b, bus.PollerOptions{Interval: cfg.Poll.Interval.Std()})
	defer poller.Close()

	unit := control.New(b, poller, cfg.Poll.Interval.Std(), cfg.Unit.Name)

	authenticator := auth.New(auth.Config(cfg.Auth))
	// Written back through a fresh load so that setting a PIN does not stamp
	// this process's idea of the rest of the file over whatever is on disk.
	authenticator.Save = func(c auth.Config) error {
		current, err := config.Load(configPath)
		if err != nil {
			return err
		}
		current.Auth = config.Auth(c)
		return current.Save(configPath)
	}
	applyPINReset(authenticator, log)
	if !authenticator.Configured() {
		log.Warn("no PIN is set, so settings and timer programs are locked; set one in the web interface")
	}

	if cfg.Unit.SyncClock {
		stop := make(chan struct{})
		defer close(stop)
		go keepClockInStep(unit, cfg.Unit, log, stop)
	}

	notifier := startNotify(cfg, log)
	watcher := notify.Watch(unit, notifier, notify.WatcherOptions{
		Interval:  cfg.Notify.Interval.Std(),
		DownAfter: cfg.Notify.DownAfter.Std(),
	})
	defer watcher.Close()

	// The supply under the box itself. Under-voltage is the failure most
	// likely to be met in a house: a Pi on whatever charger was in the drawer
	// corrupts its card, throttles, and restarts at random, and every one of
	// those looks like a different bug until somebody blames the charger.
	powerWatch := power.Options{
		Logger: log, Notifier: notifier, Unit: unitName(unit, cfg),
		Source: hostctl.PowerLog,
	}.Watch()
	defer powerWatch.Close()

	// An update that wants a restart: say so, or do it, as the owner chose.
	// Never on our own initiative — a ventilation unit that disappears at three
	// in the morning because a package was replaced is not behaviour anybody
	// asked for, which is why the default is to say so and stop there.
	rebootCtx, stopReboot := context.WithCancel(context.Background())
	defer stopReboot()
	go hostctl.WatchReboot(rebootCtx, notifier, func() hostctl.RebootPolicy {
		// Read afresh each time, so changing the setting takes effect without
		// a restart — which would be an odd thing to need from this of all
		// features.
		current, err := config.Load(configPath)
		if err != nil {
			current = cfg
		}
		h, m := hostctl.ParseRebootAt(current.Updates.RebootAt)
		now := time.Now()
		return hostctl.RebootPolicy{
			Auto: current.Updates.RebootPolicy == "auto",
			At:   time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location()),
			// The name as the interface shows it, so somebody with two of
			// these can tell which one is about to restart.
			Unit: unitName(unit, current),
		}
	}, 15*time.Minute, log)

	// One sample per bucket, saying whether the supply sagged during it — not
	// what was true at the instant the sample was taken, which on events this
	// short would read clear almost every time.
	hostSamples := func() []store.Sample {
		if !powerWatch.State().Watched {
			return nil
		}
		v := 0.0
		if powerWatch.Drain() {
			v = 1
		}
		return []store.Sample{{Metric: store.MetricUndervoltage, Value: v}}
	}

	history, recorder, err := startHistory(cfg, unit, hostSamples, log)
	if err != nil {
		return err
	}
	if recorder != nil {
		defer recorder.Close()
	}
	if history != nil {
		defer history.Close()
		// The notification log lives beside the measurements, in the same
		// file. Raspberry Pi OS keeps the system journal in memory to spare the
		// card, so a box that now restarts itself at four in the morning would
		// otherwise come back with no record of what it had said before it
		// went.
		notifier.SetJournal(eventJournal{history})
	}

	gatewaySrv, err := startGateway(cfg, b, log)
	if err != nil {
		return err
	}
	if gatewaySrv != nil {
		defer gatewaySrv.Close()
	}

	httpSrv, err := startWeb(cfg, configPath, unit, poller, history, authenticator, notifier, gatewaySrv, b, powerWatch, log)
	if err != nil {
		return err
	}
	if httpSrv != nil {
		defer func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutdown)
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	log.Info("freewayd stopping")
	logSummary(b, poller, gatewaySrv, log)
	return nil
}

func serialConfig(cfg config.Config) serial.Config {
	sc := serial.DefaultConfig(cfg.Serial.Port)
	sc.Baud = cfg.Serial.Baud
	return sc
}

// logUpdateState records at startup whether this box is still being patched.
//
// In the journal rather than only in the interface: a box nobody has looked at
// for two years is exactly the one this matters for, and journalctl is what
// somebody reaches for when they finally do.
// Runs in its own goroutine, because counting pending updates shells out to
// apt and takes about fifteen seconds on this hardware. Waiting for the real
// count matters here in a way it does not on a web page: "pending=0" in the
// journal, logged before anything had been counted, would be a plain lie in
// the one place somebody looks years later.
func logUpdateState(log *slog.Logger) {
	u := system.RefreshUpdates()
	attrs := []any{
		"pending", u.Pending, "security", u.Security,
		"unattended_upgrades", u.Unattended, "reboot_required", u.RebootRequired,
	}
	if !u.DistroEOL.IsZero() {
		attrs = append(attrs, "os_supported_until", u.DistroEOL.Format("2006-01-02"))
	}
	switch {
	case !u.Unattended:
		log.Warn("automatic security updates are not configured on this box", attrs...)
	case !u.DistroEOL.IsZero() && !u.DistroSupported:
		log.Warn("this Debian release no longer receives security updates", attrs...)
	case u.Security > 0:
		log.Info("security updates are pending", attrs...)
	default:
		log.Info("updates", attrs...)
	}
}

// logNetwork records the box's own address at startup.
//
// Useful on its own, and it proves from inside the service sandbox that nmcli
// is reachable — reading needs D-Bus, which a tightened unit file can take away
// without anything else noticing.
//
// Patient, because at boot this runs before there is an answer. The unit waits
// on network.target, which means the machinery is up, not that an address has
// been handed out; asking once put a warning in the log on every single boot
// and then never mentioned the network again, which is precisely backwards —
// the line is worth having and the warning was not.
func logNetwork(log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var last error
	for attempt := 0; ; attempt++ {
		read, done := context.WithTimeout(ctx, 10*time.Second)
		c, err := netconf.Read(read)
		done()
		if err == nil {
			log.Info("network",
				"connection", c.Connection, "device", c.Device,
				"method", map[bool]string{true: "dhcp", false: "static"}[c.DHCP],
				"address", c.CurrentAddress, "changeable_from_interface", c.Writable)
			return
		}
		last = err
		select {
		case <-ctx.Done():
			// Two minutes without an address is no longer a boot race; it is a
			// box nobody can reach, and worth saying out loud.
			log.Warn("could not read the network configuration", "err", last)
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	// Plain text to stderr: journald captures it, and its own timestamps make
	// ours redundant.
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: l,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

// reportUnit reads the clock block and the software version, so the log says
// what it is connected to rather than only that it started.
func reportUnit(b *bus.Bus, log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var clock []uint16
	var sw []uint16
	err := b.Do(ctx, bus.PriorityWrite, func(c *modbus.Client) error {
		var err error
		if clock, err = c.ReadHolding(37, 7); err != nil {
			return err
		}
		sw, err = c.ReadHolding(599, 1)
		return err
	})
	if err != nil {
		return err
	}

	log.Info("unit answered",
		"software_version", sw[0],
		"unit_clock", fmt.Sprintf("%02d:%02d:%02d", clock[2], clock[1], clock[0]),
		"unit_date", fmt.Sprintf("%d.%d.%d", clock[3], clock[4], 2000+int(clock[5])),
	)

	// The unit's clock drives its week and year programs, so a drift here is
	// not cosmetic. Writing it back is a later phase; saying so is free.
	if drift := clockDrift(clock); drift > 2*time.Minute {
		log.Warn("the unit's clock disagrees with system time; its timer programs will fire at the wrong time",
			"drift", drift.Round(time.Minute))
	}
	return nil
}

func clockDrift(clock []uint16) time.Duration {
	unitTime := time.Date(2000+int(clock[5]), time.Month(clock[4]), int(clock[3]),
		int(clock[2]), int(clock[1]), int(clock[0]), 0, time.Local)
	d := time.Since(unitTime)
	if d < 0 {
		d = -d
	}
	return d
}

func startGateway(cfg config.Config, b *bus.Bus, log *slog.Logger) (*gateway.Server, error) {
	// Always built, whether or not it is switched on. The System page turns it
	// on and off through this server, so a box that started with it off must
	// still have one — without it, the page had nothing to show but "disabled
	// in the configuration" and a Save button with nothing to save, and the
	// only way to turn on the feature this box exists for was to edit a file
	// over ssh.
	srv := gateway.New(b, gateway.Options{
		ACL: gateway.NewACL(),
		Breaker: gateway.NewBreaker(gateway.BreakerOptions{
			Threshold: cfg.Gateway.BreakerThreshold,
			Cooldown:  cfg.Gateway.BreakerCooldown.Std(),
		}),
		Logger:         log,
		RequestTimeout: cfg.Gateway.RequestTimeout.Std(),
	})

	if !cfg.Gateway.Enabled {
		log.Info("modbus gateway is switched off; the port is closed until it is turned on")
		// The list is still loaded, so turning it on later starts from what was
		// there rather than from nothing.
		if err := srv.Configure(false, cfg.Gateway.Allow, cfg.Gateway.Listen); err != nil {
			return nil, err
		}
		return srv, nil
	}
	if len(cfg.Gateway.Allow) == 0 {
		log.Warn("modbus gateway is enabled with an empty access list, so every client will be refused",
			"suggestion", gateway.SuggestLocalPrefixes())
	}
	if err := srv.Configure(true, cfg.Gateway.Allow, cfg.Gateway.Listen); err != nil {
		return nil, err
	}
	return srv, nil
}

// startNotify builds the channels from configuration. A box with none
// configured still runs; it simply has nobody to tell.
func startNotify(cfg config.Config, log *slog.Logger) *notify.Notifier {
	channels := notify.ChannelsFor(cfg.Notify)
	if len(channels) == 0 {
		log.Info("no notification channel is configured; alarms will only appear in the interface")
	}
	return notify.New(notify.Options{
		Channels:    channels,
		MinSeverity: notify.ParseSeverity(cfg.Notify.MinSeverity),
		UnitName:    cfg.Unit.Name,
		Logger:      log,
		// Parsed rather than taken as it is, so a language this build does not
		// know falls back to the default instead of to message ids.
		Language: i18n.Parse(cfg.Notify.Language, ""),
	})
}

func startHistory(cfg config.Config, unit *control.Unit, host func() []store.Sample, log *slog.Logger) (*store.Store, *store.Recorder, error) {
	if !cfg.History.Enabled {
		log.Info("history is not being recorded")
		return nil, nil, nil
	}
	if dir := filepath.Dir(cfg.History.Path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, nil, fmt.Errorf("history: create %s: %w", dir, err)
		}
	}
	db, err := store.Open(cfg.History.Path)
	if err != nil {
		return nil, nil, err
	}
	if st, err := db.Stats(); err == nil && !st.Oldest.IsZero() {
		log.Info("history opened", "path", cfg.History.Path,
			"oldest", st.Oldest.Format(time.RFC3339), "size_kb", st.SizeBytes/1024)
	} else {
		log.Info("history opened", "path", cfg.History.Path)
	}

	rec := store.NewRecorder(db, unit.State, store.RecorderOptions{
		Interval:     cfg.History.Interval.Std(),
		Housekeeping: cfg.History.Housekeeping.Std(),
		Logger:       log,
		Host:         host,
	})
	return db, rec, nil
}

// history is the concrete type rather than web.History on purpose. Passing a
// nil *store.Store into an interface parameter produces an interface that is
// not nil but holds nil, and every nil check downstream then reads false.
func startWeb(cfg config.Config, configPath string, unit *control.Unit, poller *bus.Poller, history *store.Store, authenticator *auth.Authenticator, notifier *notify.Notifier, gatewaySrv *gateway.Server, b *bus.Bus, supply web.PowerSupply, log *slog.Logger) (*http.Server, error) {
	if !cfg.Web.Enabled {
		log.Info("web interface is disabled")
		return nil, nil
	}

	// A typed nil in an interface is not nil, so a store that was never opened
	// has to be left out rather than passed along as an empty one.
	opts := web.Options{
		Logger: log, Auth: authenticator, Notifier: notifier, Bus: b, Power: supply,
		Gateway: gatewaySrv, GatewayConfig: cfg.Gateway, NotifyConfig: cfg.Notify,
		UnitConfig:    cfg.Unit,
		UpdatesConfig: cfg.Updates,
		Version:       version, Commit: commit, Built: built, StatePath: filepath.Dir(cfg.History.Path), ConfigPath: configPath,
		SaveConfig: func(apply func(*config.Config)) error {
			// Reloaded first, so saving one section does not stamp this
			// process's idea of the rest over what is on disk.
			current, err := config.Load(configPath)
			if err != nil {
				return err
			}
			apply(&current)
			return current.Save(configPath)
		},
	}
	if history != nil {
		opts.History = history
	}
	handler := web.New(unit, poller, opts)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: cfg.Web.ReadHeaderTimeout.Std(),
		// A body has to arrive in a reasonable time, and an idle keep-alive
		// connection is eventually let go rather than held for as long as the
		// peer keeps the socket open.
		ReadTimeout: 30 * time.Second,
		IdleTimeout: 2 * time.Minute,
		// No write timeout: the event stream is a long-lived response and a
		// write deadline would cut it off at the deadline, every time.
	}

	ln, err := net.Listen("tcp", cfg.Web.Listen)
	if err != nil {
		return nil, fmt.Errorf("web: listen on %s: %w", cfg.Web.Listen, err)
	}
	log.Info("web interface listening", "addr", ln.Addr().String())

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("web interface stopped", "err", err)
		}
	}()
	return srv, nil
}

// keepClockInStep corrects the unit's clock when it has drifted.
//
// It reads the drift from the snapshot already in hand rather than asking the
// unit, and writes only when the drift exceeds the tolerance, so an appliance
// that is keeping perfect time costs nothing.
func keepClockInStep(unit *control.Unit, cfg config.Unit, log *slog.Logger, stop <-chan struct{}) {
	done := make(chan struct{})
	var once sync.Once
	giveUp := func() { once.Do(func() { close(done) }) }

	check := func() {
		st, err := unit.State()
		if err != nil {
			return
		}
		if st.UnitClock.IsZero() || st.UnitClockDrift < cfg.SyncClockTolerance.Std() {
			return
		}
		// Two minutes, not thirty seconds: the write waits for a minute
		// boundary, because the unit zeroes the seconds when it applies one.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := unit.SyncClock(ctx); err != nil {
			log.Warn("could not set the unit's clock", "drift", st.UnitClockDrift.Round(time.Second), "err", err)
			if after, _ := unit.State(); after != nil && after.ClockSettable != nil && !*after.ClockSettable {
				log.Warn("this unit ignores clock writes; giving up rather than retrying every interval. " +
					"Set the clock on the control panel")
				giveUp()
			}
			return
		}
		log.Info("set the unit's clock", "drift_was", st.UnitClockDrift.Round(time.Second))
	}

	// Not immediately: the first snapshot has to land first, and a daemon that
	// writes to the unit within a second of starting is hard to interrupt if
	// it is doing the wrong thing.
	first := time.NewTimer(time.Minute)
	defer first.Stop()
	ticker := time.NewTicker(cfg.SyncClockInterval.Std())
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-done:
			return
		case <-first.C:
			check()
		case <-ticker.C:
			check()
		}
	}
}

func logSummary(b *bus.Bus, p *bus.Poller, gatewaySrv *gateway.Server, log *slog.Logger) {
	bs := b.Stats()
	ps := p.Stats()
	attrs := []any{
		"poll_passes", ps.Passes,
		"poll_failures", ps.Failures,
		"wire_attempts", bs.Exchanges,
		"wire_error_rate", fmt.Sprintf("%.3f%%", 100*bs.ErrorRate()),
	}
	if gatewaySrv != nil {
		gs := gatewaySrv.Stats()
		attrs = append(attrs, "client_requests", gs.Served, "client_refusals", gs.Refused, "breaker_trips", gs.Breaker.Trips)
	}
	log.Info("summary", attrs...)
}

// versionString is the one-line form, for the command line and the log.
func versionString() string {
	s := version
	if commit != "" {
		s += " (" + commit + ")"
	}
	if built != "" {
		s += " built " + built
	}
	return s
}

// pinResetPath is where freeway-card leaves a PIN somebody wrote on the boot
// partition. Root-owned and readable by this daemon's group; nothing writes it
// but that script, and nothing over the network can reach it.
const pinResetPath = "/run/freeway-pin-reset"

// applyPINReset sets the PIN from the card, if one was left there.
//
// The forgotten-PIN answer. It asks for no old PIN, deliberately: whoever took
// the card out of the box has physical possession, which is a stronger claim
// than any PIN — and without this the only answer to a forgotten PIN is editing
// config.json over ssh, which is exactly the thing somebody locked out cannot
// necessarily do.
//
// Read once and deleted immediately, whether it worked or not. A PIN left
// sitting in /run is a PIN anybody in the daemon's group can read at leisure.
func applyPINReset(a *auth.Authenticator, log *slog.Logger) {
	pin, err := os.ReadFile(pinResetPath)
	if err != nil {
		return // the normal case, on every boot but the one that matters
	}
	if err := os.Remove(pinResetPath); err != nil {
		log.Error("could not remove the PIN left on the boot partition", "err", err)
	}
	if err := a.ForcePIN(strings.TrimSpace(string(pin))); err != nil {
		log.Error("the PIN left on the boot partition was refused", "err", err)
		return
	}
	// Loud on purpose. Somebody changing the lock by taking the card out is
	// either the owner or a problem, and either way it belongs in the log.
	log.Warn("the PIN was reset from the boot partition; every session has ended")
}

// unitName is what to call this installation in a notification: what somebody
// configured, or what the unit calls itself, or nothing rather than a guess.
func unitName(u *control.Unit, cfg config.Config) string {
	if cfg.Unit.Name != "" {
		return cfg.Unit.Name
	}
	if st, err := u.State(); err == nil && st != nil {
		return st.Name
	}
	return ""
}

// eventJournal writes notifications into the history file.
//
// The adapter exists so that package notify knows nothing about a database and
// package store knows nothing about notifications; each has one type the other
// has never heard of, and this is the seam between them.
type eventJournal struct{ db *store.Store }

func (j eventJournal) Append(r notify.Record) error {
	return j.db.AppendEvent(store.Event{
		Time:      r.Time,
		Key:       r.Key,
		Kind:      r.Kind,
		Severity:  r.Level,
		Title:     r.Title,
		Message:   r.Message,
		Resolved:  r.Resolved,
		Delivered: r.Delivered,
		Detail:    r.Detail,
	})
}
