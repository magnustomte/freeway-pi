package web

import (
	"fmt"
	"net/http"
	"strings"

	"freewaypi/internal/eda"
	"freewaypi/internal/i18n"
)

// handleSettings lists what can be changed and what it currently is.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	values, err := s.ctrl.Settings()
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, err)
		return
	}
	// The eda package carries ids where the words go; they are filled in here,
	// where the request has said which language to fill them in.
	lang := langOf(r)
	for i := range values {
		values[i].Label = i18n.T(lang, values[i].Label)
		values[i].Group = i18n.T(lang, values[i].Group)
		// Symbols such as °C and % pass through; a unit that is a word has a
		// catalogue id, and only those start with "ui.".
		if strings.HasPrefix(values[i].Unit, "ui.") {
			values[i].Unit = i18n.T(lang, values[i].Unit)
		}
		if values[i].Help != "" {
			values[i].Help = i18n.T(lang, values[i].Help)
		}
	}
	writeJSON(w, http.StatusOK, values)
}

// handleSetSetting applies one change.
//
// One at a time on purpose. Each is a separate write to a unit that takes a
// noticeable moment per write, and a form that saved twelve settings at once
// would sit there for several seconds with no way to say which of them failed.
func (s *Server) handleSetSetting(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key    string   `json:"key"`
		Number *float64 `json:"number"`
		Bool   *bool    `json:"bool"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}

	def, ok := eda.SettingByKey(body.Key)
	if !ok {
		writeError(w, r, http.StatusBadRequest, i18n.Errf("error.setting.unknown", body.Key))
		return
	}
	if def.Type == "bool" && body.Bool == nil {
		writeError(w, r, http.StatusBadRequest, i18n.Errf("error.setting.bool", i18n.Msg(def.Label)))
		return
	}
	if def.Type == "number" && body.Number == nil {
		writeError(w, r, http.StatusBadRequest, i18n.Errf("error.setting.number", i18n.Msg(def.Label)))
		return
	}

	var number float64
	var boolean bool
	if body.Number != nil {
		number = *body.Number
	}
	if body.Bool != nil {
		boolean = *body.Bool
	}

	writes, err := eda.WriteSetting(body.Key, number, boolean)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := contextWithTimeout(r)
	defer cancel()
	if err := s.ctrl.Apply(ctx, writes); err != nil {
		s.log.Warn("setting failed", "key", body.Key, "err", err)
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	for _, write := range writes {
		s.log.Info("setting changed", "change", write.Describe, "from", r.RemoteAddr)
	}
	s.handleSettings(w, r)
}

// handleServiceReset puts the days-since-service counter back to zero.
//
// Its own action rather than a settings row, because it is an event and not a
// value: somebody changed the filter today. Changing the interval does not do
// this — the two are separate registers, and a new interval only recomputes the
// countdown from the same number of days already run.
func (s *Server) handleServiceReset(w http.ResponseWriter, r *http.Request) {
	if err := sameSite(r); err != nil {
		writeError(w, r, http.StatusForbidden, err)
		return
	}
	ctx, cancel := contextWithTimeout(r)
	defer cancel()

	if err := s.ctrl.Apply(ctx, eda.ResetServiceCounter()); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	s.log.Info("service counter reset", "from", r.RemoteAddr)

	st, err := s.ctrl.State()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": "Nullstilt."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"note":    fmt.Sprintf("Nullstilt. Neste service om %d dager.", st.Service.IntervalDays),
		"service": st.Service,
	})
}
