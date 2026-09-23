package web

import (
	"net/http"
	"strconv"
	"time"

	"freewaypi/internal/i18n"

	"freewaypi/internal/eda"
)

// ClockDriftLimit is how far the unit's clock may be out before the interface
// stops letting timer programs be edited without a word.
//
// Timer programs fire by the unit's own clock, not by ours. Editing a schedule
// on a unit whose clock is two hours out produces something that runs at the
// wrong time, and nothing about the result looks wrong until somebody notices
// the house is cold at the wrong hour.
const ClockDriftLimit = 5 * time.Minute

type programsResponse struct {
	// Running is coil 43: a timer program is in effect at this moment.
	// The unit has no global switch for timer programs, and a write to
	// that coil is answered with server device failure. A program is off
	// when its function is none.
	Running bool                 `json:"running"`
	Week    []eda.WeekProgram    `json:"week"`
	Year    []eda.YearProgram    `json:"year"`
	Choices []eda.FunctionChoice `json:"choices"`
	FanMin  int                  `json:"fan_min"`
	FanMax  int                  `json:"fan_max"`
	// ClockDriftSeconds is signed, so the interface can say which way.
	ClockDriftSeconds float64 `json:"clock_drift_seconds"`
	ClockOK           bool    `json:"clock_ok"`
	ClockDriftIsDST   bool    `json:"clock_drift_is_dst"`
}

func (s *Server) handleProgramsGet(w http.ResponseWriter, r *http.Request) {
	week, year, err := s.ctrl.Programs()
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, err)
		return
	}
	st, err := s.ctrl.State()
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, err)
		return
	}
	lang := langOf(r)
	choices := make([]eda.FunctionChoice, len(eda.FunctionChoices))
	for i, c := range eda.FunctionChoices {
		c.Label = i18n.T(lang, c.Label)
		if c.Help != "" {
			c.Help = i18n.T(lang, c.Help)
		}
		choices[i] = c
	}
	writeJSON(w, http.StatusOK, programsResponse{
		Running: st.TimeProgramRunning, Week: week, Year: year,
		Choices: choices, FanMin: eda.FanMin, FanMax: eda.FanMax,
		ClockDriftSeconds: st.DriftSeconds,
		ClockOK:           st.UnitClockDrift < ClockDriftLimit,
		ClockDriftIsDST:   st.ClockDriftIsDST,
	})
}

// clockGuard refuses an edit while the unit's clock is wrong, unless the caller
// has said so deliberately.
//
// The interface offers to set the clock as the easy answer and this override as
// the deliberate one. A guard that is merely clicked through is not a guard.
func (s *Server) clockGuard(acknowledged bool) error {
	if acknowledged {
		return nil
	}
	st, err := s.ctrl.State()
	if err != nil {
		return err
	}
	if st.UnitClock.IsZero() {
		return i18n.Errf("error.clock.unreadable")
	}
	if st.UnitClockDrift >= ClockDriftLimit {
		return i18n.Errf("error.clock.drift", st.UnitClockDrift.Round(time.Minute))
	}
	return nil
}

func (s *Server) handleWeekProgramPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Program               eda.WeekProgram `json:"program"`
		AcknowledgeClockDrift bool            `json:"acknowledge_clock_drift"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := s.clockGuard(body.AcknowledgeClockDrift); err != nil {
		writeError(w, r, http.StatusConflict, err)
		return
	}
	writes, err := eda.WriteWeekProgram(body.Program)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.applyPrograms(w, r, writes)
}

func (s *Server) handleYearProgramPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Program               eda.YearProgram `json:"program"`
		AcknowledgeClockDrift bool            `json:"acknowledge_clock_drift"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := s.clockGuard(body.AcknowledgeClockDrift); err != nil {
		writeError(w, r, http.StatusConflict, err)
		return
	}
	writes, err := eda.WriteYearProgram(body.Program)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.applyPrograms(w, r, writes)
}

func (s *Server) applyPrograms(w http.ResponseWriter, r *http.Request, writes []eda.Write) {
	ctx, cancel := contextWithTimeout(r)
	defer cancel()
	if err := s.ctrl.Apply(ctx, writes); err != nil {
		s.log.Warn("timer program write failed", "err", err)
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	for _, write := range writes {
		s.log.Info("timer program changed", "change", write.Describe, "from", r.RemoteAddr)
	}
	s.handleProgramsGet(w, r)
}

// handleProgramDelete blanks a program.
//
// Deliberately not behind the clock guard. That guard exists because a schedule
// set against a wrong clock runs at the wrong time; removing a schedule cannot
// do anything at the wrong time, and refusing it would leave someone unable to
// undo the very thing the guard is worried about.
func (s *Server) handleProgramDelete(week bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		index, err := strconv.Atoi(r.URL.Query().Get("index"))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, i18n.Errf("error.program.index"))
			return
		}
		var writes []eda.Write
		if week {
			writes, err = eda.ClearWeekProgram(index)
		} else {
			writes, err = eda.ClearYearProgram(index)
		}
		if err != nil {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
		s.applyPrograms(w, r, writes)
	}
}
