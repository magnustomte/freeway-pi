package web

import (
	"net/http"
	"time"

	"freewaypi/internal/config"
)

// clockConfig is the unit's clock as the settings page sees it: what it reads
// now, how far out it is, and whether the box keeps it right by itself.
//
// Not one of the register-backed settings, because none of it lives in the
// unit. The box's own clock comes from NTP; this is about what it does with it.
type clockConfig struct {
	UnitClock time.Time `json:"unit_clock"`
	// UnitClockAge is how long ago UnitClock was read, so the page can show
	// the three clocks side by side without the unit's looking a poll behind.
	UnitClockAge float64 `json:"unit_clock_age_seconds"`
	DriftSeconds float64 `json:"drift_seconds"`
	// Settable is null until a write has been tried, false if one was taken
	// and ignored.
	Settable *bool `json:"settable"`

	AutoSync bool `json:"auto_sync"`
	// Interval is how often the box checks, and Tolerance how far out the
	// clock may be before it corrects it.
	Interval  string `json:"interval"`
	Tolerance string `json:"tolerance"`
	// SystemTime is this box's own idea of the time, so a clock that is being
	// kept in step with something wrong is visible as such.
	SystemTime time.Time `json:"system_time"`
}

func (s *Server) handleClockGet(w http.ResponseWriter, r *http.Request) {
	out := clockConfig{
		AutoSync:   s.unitConfig.SyncClock,
		Interval:   s.unitConfig.SyncClockInterval.Std().String(),
		Tolerance:  s.unitConfig.SyncClockTolerance.Std().String(),
		SystemTime: time.Now(),
	}
	if st, err := s.ctrl.State(); err == nil {
		out.UnitClock = st.UnitClock
		out.UnitClockAge = time.Since(st.Taken).Seconds()
		out.DriftSeconds = st.DriftSeconds
		out.Settable = st.ClockSettable
	}
	writeJSON(w, http.StatusOK, out)
}

// handleClockPut turns automatic correction on or off.
//
// Only the switch. The interval and the tolerance are in the configuration file
// for somebody who wants them different, and putting two more numbers on the
// page would be three controls for a thing most people want either on or off.
func (s *Server) handleClockPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AutoSync   bool   `json:"auto_sync"`
		ReaderWall string `json:"reader_wall"`
		Confirmed  bool   `json:"confirmed"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	// Turning it on is a clock write every few hours with nobody watching, so
	// it gets the same question a single write does.
	if body.AutoSync && !s.unitConfig.SyncClock && !body.Confirmed {
		if suspect, off := s.suspectClockWrite(body.ReaderWall); suspect {
			askFirst(w, r, off)
			return
		}
	}
	if err := s.saveConfig(func(c *config.Config) {
		c.Unit.SyncClock = body.AutoSync
	}); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.unitConfig.SyncClock = body.AutoSync
	s.log.Info("clock synchronisation changed", "auto_sync", body.AutoSync, "from", r.RemoteAddr)

	note := t(r, "note.clock.off")
	if body.AutoSync {
		note = t(r, "note.clock.on",
			s.unitConfig.SyncClockTolerance.Std(), s.unitConfig.SyncClockInterval.Std())
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": note})
}

// agree is how close two clocks have to be to count as telling the same time.
// Wide enough for a snapshot that is a poll interval old and a browser whose
// clock is a little out; narrow enough that a wrong timezone, which is at
// least half an hour, is never mistaken for agreement.
const agree = 5 * time.Minute

// clockSuspect reports whether setting the unit's clock from this box would
// probably make it wrong.
//
// The box writes its own local time into the unit, and a box still on the
// image's timezone — Europe/London, whatever country it is in — writes a time
// that is an hour or more out. The person asking has a third clock. If the
// unit agrees with that one and the box does not, the box is the one that is
// wrong, and the write would copy its mistake into the clock the timer
// programmes run by. It has happened.
//
// A suspicion, not a verdict: the browser may be on a laptop in another
// country, so the caller asks rather than refuses.
func clockSuspect(unitNow, boxNow, readerNow time.Time) (bool, time.Duration) {
	off := boxNow.Sub(readerNow)
	if off < 0 {
		off = -off
	}
	unit := unitNow.Sub(readerNow)
	if unit < 0 {
		unit = -unit
	}
	return unit < agree && off >= agree, off
}

// readerWall parses the reader's wall clock as the browser sends it: the
// digits of its local time with no zone, so they compare with the unit's
// clock, which has no zone either.
func readerWall(s string) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01-02T15:04:05", s, time.Local)
	return t, err == nil
}

// suspectClockWrite applies clockSuspect to the unit as it is now. No reader
// clock — a script, an older page — means no opinion, and the write goes
// ahead as it always did.
func (s *Server) suspectClockWrite(reader string) (bool, time.Duration) {
	readerNow, ok := readerWall(reader)
	if !ok {
		return false, 0
	}
	st, err := s.ctrl.State()
	if err != nil || st.UnitClock.IsZero() {
		return false, 0
	}
	// The unit's clock as it will be now, not as it was when last read.
	unitNow := st.UnitClock.Add(time.Since(st.Taken))
	// The box's wall clock in the same zoneless terms as the other two.
	now := time.Now()
	boxNow := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), 0, time.Local)
	return clockSuspect(unitNow, boxNow, readerNow)
}

// askFirst answers a clock write that looks like it would make the unit wrong.
// 409, because the request is fine and the state of the box is what stands in
// the way; needs_confirm tells the page to ask and send it again.
func askFirst(w http.ResponseWriter, r *http.Request, off time.Duration) {
	var span string
	if h := int(off.Round(time.Hour) / time.Hour); h >= 1 {
		span = t(r, map[bool]string{true: "ui.hours", false: "ui.hours.plural"}[h == 1], h)
	} else {
		span = t(r, "ui.minutes.n", int(off.Round(time.Minute)/time.Minute))
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"needs_confirm": true,
		"error":         t(r, "clock.confirm.zone", span),
	})
}
