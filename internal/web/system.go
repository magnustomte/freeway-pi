package web

import (
	"context"
	"freewaypi/internal/hostctl"
	"net/http"
	"time"

	"strings"

	"freewaypi/internal/bus"
	"freewaypi/internal/config"
	"freewaypi/internal/eda"
	"freewaypi/internal/gateway"
	"freewaypi/internal/i18n"
	"freewaypi/internal/notify"
	"freewaypi/internal/store"
	"freewaypi/internal/system"
)

// systemResponse is everything the system page shows in one request, so the
// figures on it are all from the same moment.
type systemResponse struct {
	Version string      `json:"version"`
	Commit  string      `json:"commit,omitempty"`
	Built   string      `json:"built,omitempty"`
	Host    system.Info `json:"host"`

	Bus     bus.Stats       `json:"bus"`
	Poll    pollSummary     `json:"poll"`
	History *store.Stats    `json:"history,omitempty"`
	Gateway *gatewaySummary `json:"gateway,omitempty"`
	Notify  notifySummary   `json:"notify"`
	Supply  supplyView      `json:"supply"`
	Unit    unitSummary     `json:"unit"`
	Admin   adminSummary    `json:"admin"`
}

type pollSummary struct {
	Passes   uint64  `json:"passes"`
	Failures uint64  `json:"failures"`
	Interval string  `json:"interval"`
	AgeSecs  float64 `json:"snapshot_age_seconds"`
	LastErr  string  `json:"last_error,omitempty"`
}

type gatewaySummary struct {
	Enabled bool                 `json:"enabled"`
	Listen  string               `json:"listen"`
	Allow   []string             `json:"allow"`
	Clients []gateway.ClientInfo `json:"clients"`
	Stats   gateway.ServerStats  `json:"stats"`
	// Suggested is this machine's own networks, offered when the list is empty
	// so nobody has to type a subnet from memory.
	Suggested []string `json:"suggested"`
}

type notifySummary struct {
	notify.Stats
	Events []notify.Event `json:"active_events"`
	// Logged is how many are in the history behind the card, so it can say so
	// without fetching them.
	Logged int `json:"logged"`
}

type unitSummary struct {
	Name string `json:"name"`
	// ConfiguredName is what is in the configuration, which is empty when the
	// name on screen is the model the unit reported. The field shows the one
	// and the placeholder shows the other, so clearing it is a way back to
	// the discovered name rather than a way to a blank label.
	ConfiguredName  string  `json:"configured_name"`
	Discovered      string  `json:"discovered_name,omitempty"`
	SoftwareVersion int     `json:"software_version"`
	LinkStatus      string  `json:"link_status"`
	ClockDriftSecs  float64 `json:"clock_drift_seconds"`
	ClockSettable   *bool   `json:"clock_settable"`
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	out := systemResponse{
		Version: s.version,
		Commit:  s.commit,
		Built:   s.built,
		Host:    system.Read(s.statePath),
		Bus:     s.busStats(),
		Notify:  notifySummary{Stats: s.notifier.Stats(), Events: s.notifier.Active()},
		Supply:  s.supply(),
		Admin:   s.adminSummary(r),
	}

	if s.poller != nil {
		ps := s.poller.Stats()
		out.Poll = pollSummary{
			Passes: ps.Passes, Failures: ps.Failures,
			Interval: ps.Interval.String(),
		}
		if ps.LastErr != nil {
			out.Poll.LastErr = ps.LastErr.Error()
		}
		if snap := s.poller.Current(); snap != nil {
			out.Poll.AgeSecs = snap.Age().Seconds()
		}
	}
	if s.history != nil {
		if hs, err := s.history.Stats(); err == nil {
			out.History = hs
		}
		if n, err := s.history.CountEvents(time.Now().AddDate(0, 0, -logDays)); err == nil {
			out.Notify.Logged = n
		}
	}
	if s.gateway != nil {
		g := &gatewaySummary{
			Enabled: s.gateway.Listening(),
			Listen:  s.gatewayConfig.Listen,
			Allow:   s.gateway.Allow(),
			Clients: s.gateway.Clients(),
			Stats:   s.gateway.Stats(),
		}
		for _, p := range gateway.SuggestLocalPrefixes() {
			g.Suggested = append(g.Suggested, p.String())
		}
		out.Gateway = g
	}
	if st, err := s.ctrl.State(); err == nil {
		out.Unit = unitSummary{
			Name:            st.Name,
			ConfiguredName:  s.unitConfig.Name,
			Discovered:      st.Machine.Family,
			SoftwareVersion: st.SoftwareVersion,
			LinkStatus:      st.Link.Status, ClockDriftSecs: st.DriftSeconds,
			ClockSettable: st.ClockSettable,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// alarmView is one log entry with its words filled in.
//
// The eda package holds no sentences — a sentence has a language — so the names
// and remedies are looked up here, where the request has said which one.
type alarmView struct {
	eda.Alarm
	Name       string `json:"name"`
	Help       string `json:"help,omitempty"`
	StateLabel string `json:"state_label"`
}

// handleAlarms returns the unit's own log of the last twenty events.
func (s *Server) handleAlarms(w http.ResponseWriter, r *http.Request) {
	alarms, err := s.ctrl.Alarms()
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, s.alarmViews(r, alarms))
}

func (s *Server) alarmViews(r *http.Request, alarms []eda.Alarm) []alarmView {
	lang := langOf(r)
	out := make([]alarmView, 0, len(alarms))
	for _, a := range alarms {
		v := alarmView{Alarm: a, StateLabel: i18n.T(lang, a.State.StateID())}
		if !a.Empty {
			// A code the manual's table never named is still reported, by its
			// number, rather than dropped or rendered as a message id.
			if i18n.Has(lang, a.NameID()) {
				v.Name = i18n.T(lang, a.NameID())
			} else {
				v.Name = i18n.T(lang, "alarm.unknown", a.Code)
			}
			if i18n.Has(lang, a.HelpID()) {
				v.Help = i18n.T(lang, a.HelpID())
			}
		}
		out = append(out, v)
	}
	return out
}

// handleGatewayPut changes who may reach the Modbus gateway.
//
// Applied on the next start rather than live: the listener and the access list
// are built at startup, and restarting the service to pick them up is a second
// of downtime on a box whose clients reconnect by themselves.
func (s *Server) handleGatewayPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool     `json:"enabled"`
		Allow   []string `json:"allow"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	// Checked before saving: a file that cannot be parsed at the next start is
	// a box that does not come back.
	if _, err := gateway.ParseEntries(body.Allow); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if body.Enabled && len(body.Allow) == 0 {
		writeError(w, r, http.StatusBadRequest,
			i18n.Errf("error.gateway.empty"))
		return
	}

	if err := s.saveConfig(func(c *config.Config) {
		c.Gateway.Enabled = body.Enabled
		c.Gateway.Allow = body.Allow
	}); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("modbus gateway configuration changed", "enabled", body.Enabled, "allow", body.Allow, "from", r.RemoteAddr)

	// Applied now as well as saved. Written first, so a box that is restarted
	// before this returns comes back the way it was asked to.
	if s.gateway != nil {
		if err := s.gateway.Configure(body.Enabled, body.Allow, s.gatewayConfig.Listen); err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"note": t(r, "note.gateway.applied"),
	})
}

// handleNotifyTest sends one message through every configured channel.
func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	if err := sameSite(r); err != nil {
		writeError(w, r, http.StatusForbidden, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.notifier.Test(ctx); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// busStats reports the wire counters, or nothing when the server was built
// without a bus, as the tests are.
func (s *Server) busStats() bus.Stats {
	if s.bus == nil {
		return bus.Stats{}
	}
	return s.bus.Stats()
}

// handleUnitName changes what the interface calls the unit.
//
// Applied at once rather than at the next start: it is a label, nothing acts on
// it, and a name that only appears after a restart is a name somebody types
// twice.
func (s *Server) handleUnitName(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	if len([]rune(name)) > 40 {
		writeError(w, r, http.StatusBadRequest, i18n.Errf("error.name.toolong"))
		return
	}
	if err := s.saveConfig(func(c *config.Config) { c.Unit.Name = name }); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.unitConfig.Name = name
	s.ctrl.SetName(name)
	s.log.Info("unit name changed", "name", name, "from", r.RemoteAddr)

	note := t(r, "note.saved")
	if name == "" {
		note = t(r, "note.name.cleared")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": note})
}

// The box's own clock.
//
// Its own card because it is not a setting of the ventilation unit — it is
// what the unit's clock gets set from, and a box on the image's default
// timezone writes an hour's error into every timer programme without anything
// on screen saying so.

func (s *Server) handleTimeGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"time": hostctl.ReadTime(r.Context())})
}

func (s *Server) handleTimePut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Zone    string   `json:"zone"`
		Servers []string `json:"servers"`
		SetNTP  bool     `json:"set_ntp"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	var err error
	if body.SetNTP {
		s.log.Info("setting the time servers", "servers", body.Servers, "from", r.RemoteAddr)
		err = hostctl.SetNTP(r.Context(), body.Servers)
	} else {
		s.log.Info("setting the timezone", "zone", body.Zone, "from", r.RemoteAddr)
		var restarting bool
		restarting, err = hostctl.SetZone(r.Context(), body.Zone)
		if err == nil && restarting {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "restarting": true,
				"note": t(r, "note.time.restarting"),
				"time": hostctl.ReadTime(r.Context()),
			})
			return
		}
	}
	if err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": hostctl.ReadTime(r.Context())})
}

// handleRebootPolicy stores what to do about an update that wants a restart.
//
// A choice and not a default: Freeway Pi will not restart a ventilation unit on
// its own initiative, and equally will not leave a box needing one for weeks
// because nobody opened this page.
func (s *Server) handleRebootPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Policy string `json:"policy"`
		At     string `json:"at"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if body.Policy != "notify" && body.Policy != "auto" {
		writeError(w, r, http.StatusBadRequest, i18n.Errf("error.reboot.policy"))
		return
	}
	if body.At != "" {
		if _, err := time.Parse("15:04", body.At); err != nil {
			writeError(w, r, http.StatusBadRequest, i18n.Errf("error.reboot.at"))
			return
		}
	}
	if err := s.saveConfig(func(c *config.Config) {
		c.Updates.RebootPolicy = body.Policy
		c.Updates.RebootAt = body.At
	}); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.updatesConfig.RebootPolicy = body.Policy
	s.updatesConfig.RebootAt = body.At
	s.log.Info("reboot policy set", "policy", body.Policy, "at", body.At, "from", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
