package web

import (
	"net/http"

	"freewaypi/internal/i18n"

	"freewaypi/internal/hostctl"
	"freewaypi/internal/system"
)

// adminSummary says whether the box can be updated and restarted from here,
// and how the last attempt went.
type adminSummary struct {
	// RebootPolicy is "notify" or "auto", and RebootAt when under "auto". The
	// card shows which, because a box that will restart itself at four in the
	// morning ought to say so somewhere.
	RebootPolicy string `json:"reboot_policy"`
	RebootAt     string `json:"reboot_at"`

	Available bool   `json:"available"`
	Why       string `json:"why,omitempty"`
	// Apt is nil when the helper is not reachable, which is also when the
	// buttons are not offered.
	Apt *hostctl.Apt `json:"apt,omitempty"`
}

// adminSummary asks the helper once rather than twice.
//
// Every call starts a systemd service instance, and this one runs on every
// refresh of the system page. A status that comes back at all is proof enough
// that the helper is reachable, so availability is read from the same answer.
func (s *Server) adminSummary(r *http.Request) adminSummary {
	a, err := hostctl.Status(r.Context())
	if err != nil {
		return adminSummary{Why: err.Error(), RebootPolicy: s.rebootPolicy(), RebootAt: s.updatesConfig.RebootAt}
	}
	return adminSummary{Available: true, Apt: a,
		RebootPolicy: s.rebootPolicy(), RebootAt: s.updatesConfig.RebootAt}
}

// handleApt starts one of the two apt jobs: a look for what is available, or
// an install of it.
//
// It returns as soon as the job has started. Either takes minutes; holding the
// request open that long would time out in the browser and leave nobody able to
// say whether it worked. The page watches the status instead.
func (s *Server) handleApt(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeError(w, r, http.StatusConflict, err)
		return
	}
	var body struct {
		Task string `json:"task"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := hostctl.StartApt(r.Context(), body.Task); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	s.log.Info("apt job started", "task", body.Task, "from", r.RemoteAddr)

	note := t(r, "note.apt.refresh")
	if body.Task == hostctl.TaskUpgrade {
		note = t(r, "note.apt.upgrade")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": note})
}

// handleAptStatus is polled while a job runs, which the system page does far
// more often than it refreshes everything else.
func (s *Server) handleAptStatus(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeError(w, r, http.StatusConflict, err)
		return
	}
	a, err := hostctl.Status(r.Context())
	if err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	// Once a job has finished, the count from before it ran is a lie, and the
	// obvious question is what changed. So it is recounted here rather than
	// left to expire on its own — which matters most after a refresh, whose
	// whole purpose is to change the count.
	if !a.Running && a.Finished {
		go system.RefreshUpdates()
	}
	writeJSON(w, http.StatusOK, a)
}

// handlePower restarts or stops the box.
//
// The action is named in the body as well as the path. A page left open on a
// phone in a pocket should not be one stray request away from shutting the
// ventilation controller down, and an empty POST is exactly what a confused
// client sends.
func (s *Server) handlePower(what string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.requireAdmin(r); err != nil {
			writeError(w, r, http.StatusConflict, err)
			return
		}
		var body struct {
			Confirm string `json:"confirm"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
		if body.Confirm != what {
			writeError(w, r, http.StatusBadRequest,
				i18n.Errf("error.confirm.missing"))
			return
		}

		var err error
		if what == "reboot" {
			err = hostctl.Reboot(r.Context())
		} else {
			err = hostctl.Poweroff(r.Context())
		}
		if err != nil {
			writeError(w, r, http.StatusBadGateway, err)
			return
		}
		s.log.Warn("power operation requested", "what", what, "from", r.RemoteAddr)

		note := t(r, "note.reboot")
		if what == "poweroff" {
			note = t(r, "note.poweroff")
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": note})
	}
}

// requireAdmin refuses before doing anything when the helper is not installed,
// so the answer is "this is not enabled" rather than a sudo error.
func (s *Server) requireAdmin(r *http.Request) error {
	ok, why := hostctl.Available(r.Context())
	if ok {
		return nil
	}
	return i18n.Errf("error.admin.disabled", why)
}

// rebootPolicy names the default rather than returning an empty string, so the
// interface does not have to know what empty means.
func (s *Server) rebootPolicy() string {
	if s.updatesConfig.RebootPolicy == "auto" {
		return "auto"
	}
	return "notify"
}
