package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"freewaypi/internal/control"
	"freewaypi/internal/eda"
	"freewaypi/internal/i18n"
)

// applyTimeout bounds one control action. Generous: a mode change is two
// writes, and the unit can take the better part of a second for each.
const applyTimeout = 15 * time.Second

// contextWithTimeout bounds one write against the unit.
func contextWithTimeout(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), applyTimeout)
}

// sameSite refuses a request that a browser says came from somewhere else.
//
// For the handlers that take no body, where the Content-Type check below has
// nothing to bite on. Sec-Fetch-Site is sent by every current browser and
// cannot be set by script; Origin is the fallback. A request with neither —
// curl, a script, the Homey — is allowed through, because this defends against
// a page in a browser and not against somebody who can reach the network.
func sameSite(r *http.Request) error {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "same-site", "none", "":
	default:
		return i18n.Errf("error.crosssite")
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
			return i18n.Errf("error.crosssite")
		}
	}
	return nil
}

// decodeBody reads a small JSON body. The limit is there because this endpoint
// is unauthenticated by design.
func decodeBody(r *http.Request, v any) error {
	if err := sameSite(r); err != nil {
		return err
	}
	// application/json is not a CORS simple request, so a browser will not
	// send one cross-origin without a preflight — and the preflight fails.
	// Without this check a page on any site the phone happens to open could
	// POST text/plain here and stop the fans: the everyday controls are
	// deliberately unauthenticated, which is a decision about who is in the
	// house, not an invitation to whoever is in the browser.
	ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	if strings.TrimSpace(ct) != "application/json" {
		return i18n.Errf("error.contenttype")
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("could not read the request: %w", err)
	}
	return nil
}

// apply runs the writes and answers with the state afterwards, so the page
// shows what the unit actually did rather than what it was asked to do.
func (s *Server) apply(w http.ResponseWriter, r *http.Request, writes []eda.Write, what string) {
	if len(writes) == 0 {
		// Nothing to do is a success, not an error: pressing the mode you are
		// already in should not produce a red message.
		s.handleState(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), applyTimeout)
	defer cancel()

	if err := s.ctrl.Apply(ctx, writes); err != nil {
		s.log.Warn("control action failed", "what", what, "err", err)
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	for _, write := range writes {
		s.log.Info("control action", "action", write.Describe, "from", r.RemoteAddr)
	}
	s.handleState(w, r)
}

func (s *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	st, err := s.ctrl.State()
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, err)
		return
	}
	writes, err := eda.SetMode(st.Mode, eda.Mode(body.Mode))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.apply(w, r, writes, "mode")
}

func (s *Server) handleSetpoint(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Celsius float64 `json:"celsius"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	st, err := s.ctrl.State()
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, err)
		return
	}
	writes, err := eda.SetSetpoint(body.Celsius, st.SetpointMin, st.SetpointMax)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.apply(w, r, writes, "setpoint")
}

func (s *Server) handleFan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Percent int `json:"percent"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	st, err := s.ctrl.State()
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, err)
		return
	}
	writes, err := eda.SetFanLevel(st.ECFans, body.Percent)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.apply(w, r, writes, "fan")
}

// handleClock sets the unit's clock from system time. Offered from the banner
// that reports the drift, because a warning you can act on beats one you have
// to go and find a setting for.
// handleClock sets the unit's clock, and answers before it has.
//
// The write has to land on a minute boundary — the unit's setting block has no
// seconds register and zeroes the seconds when it applies the write — so it
// waits up to a minute. Holding the request open for that would look like a
// button that does nothing, so this says when it will happen and lets the
// drift on the page confirm it afterwards.
func (s *Server) handleClock(w http.ResponseWriter, r *http.Request) {
	// The body is optional: the reader's own clock, and whether they have
	// already been asked. A request without one is judged by the same-site
	// check alone, as it always was.
	var body struct {
		ReaderWall string `json:"reader_wall"`
		Confirmed  bool   `json:"confirmed"`
	}
	if r.ContentLength != 0 {
		if err := decodeBody(r, &body); err != nil {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
	} else if err := sameSite(r); err != nil {
		writeError(w, r, http.StatusForbidden, err)
		return
	}
	if !body.Confirmed {
		if suspect, off := s.suspectClockWrite(body.ReaderWall); suspect {
			askFirst(w, r, off)
			return
		}
	}
	target := control.NextClockWrite(time.Now())
	wait := int(time.Until(target).Round(time.Second).Seconds())

	// Its own context, not the request's: the request is answered immediately
	// and cancelling it must not cancel the write.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	from := r.RemoteAddr
	go func() {
		defer cancel()
		if err := s.ctrl.SyncClock(ctx); err != nil {
			s.log.Warn("clock sync failed", "err", err, "from", from)
			return
		}
		s.log.Info("control action", "action", "set the unit's clock", "from", from)
	}()

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"note":         t(r, "note.clock.scheduled", wait),
		"wait_seconds": wait,
	})
}

func (s *Server) handleOverpressure(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Minutes int  `json:"minutes"`
		On      bool `json:"on"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	st, err := s.ctrl.State()
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, err)
		return
	}

	var writes []eda.Write
	if body.On {
		minutes := body.Minutes
		if minutes == 0 {
			minutes = st.OverpressureMinutes
		}
		writes, err = eda.StartOverpressure(st.Mode, minutes)
	} else {
		writes, err = eda.SetMode(st.Mode, eda.ModeNormal)
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.apply(w, r, writes, "overpressure")
}

// handleBoost starts or stops boost, which the manual calls forcering.
//
// The same shape as overpressure, because they are the same kind of thing: a
// timed mode with a duration the unit stores but does not count down. The one
// difference is the register — overpressure's duration lives in two mirrored
// registers and boost's in one.
func (s *Server) handleBoost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Minutes int  `json:"minutes"`
		On      bool `json:"on"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	st, err := s.ctrl.State()
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, err)
		return
	}

	var writes []eda.Write
	if body.On {
		minutes := body.Minutes
		if minutes == 0 {
			minutes = st.BoostMinutes
		}
		writes, err = eda.StartBoost(st.Mode, minutes)
	} else {
		writes, err = eda.SetMode(st.Mode, eda.ModeNormal)
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.apply(w, r, writes, "boost")
}
