package web

import (
	"net/http"
	"time"

	"freewaypi/internal/power"
)

// PowerSupply is what the interface needs to know about the box's own supply.
type PowerSupply interface {
	State() power.State
}

// RecentlySagged is how long after an event the supply is still called bad.
//
// Under-voltage comes and goes with whatever the fan is doing, so the message
// has to outlast the event by a good margin or it would flash up for a second
// and be gone before anybody looked. It is the same span the notifier waits
// before calling the condition over, so the box does not say two things.
const RecentlySagged = time.Hour

// supplyView is the supply as the interface sees it.
type supplyView struct {
	power.State
	// Recent is true while the last event is still worth a red box.
	Recent bool `json:"recent"`
	// WithinSeconds says how long an event stays recent, so the page does not
	// have to carry its own copy of the number.
	WithinSeconds int `json:"within_seconds"`
}

func (s *Server) supply() supplyView {
	v := supplyView{WithinSeconds: int(RecentlySagged.Seconds())}
	if s.power == nil {
		return v
	}
	v.State = s.power.State()
	v.Recent = v.Now || (v.Events > 0 && time.Since(v.Last) < RecentlySagged)
	return v
}

// handleSupply reports whether the box is being starved of power.
//
// Its own endpoint rather than a field on the state: the state needs the unit
// to have answered, and a supply bad enough to matter is exactly what stops it
// answering. A message that disappears when the problem gets worse is not a
// message.
func (s *Server) handleSupply(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.supply())
}
