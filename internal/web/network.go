package web

import (
	"net/http"
	"time"

	"freewaypi/internal/netconf"
)

// NetworkRevertAfter is how long the browser has to come back on the new
// settings and confirm before the old ones return.
//
// Long enough for an address change plus a page load on a phone that has to
// notice the network changed; short enough that nobody stands in a utility room
// wondering whether it is coming back.
const NetworkRevertAfter = 90 * time.Second

func (s *Server) handleNetworkGet(w http.ResponseWriter, r *http.Request) {
	// ?connection= picks which one to show. Absent means the one carrying
	// traffic, which is what anybody arriving at the page wants to see.
	c, err := netconf.ReadConnection(r.Context(), r.URL.Query().Get("connection"))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"config":               c,
		"pending":              netconf.Pending(r.Context()),
		"revert_after_seconds": int(NetworkRevertAfter.Seconds()),
	})
}

// handleNetworkPut changes the settings, with a revert armed behind it.
func (s *Server) handleNetworkPut(w http.ResponseWriter, r *http.Request) {
	var body netconf.Config
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}

	// The connection named by the browser has to be one that is actually up:
	// a name straight through to the helper would be a string from the network
	// deciding what root reconfigures. Empty still means the primary one.
	current, err := netconf.ReadConnection(r.Context(), body.Connection)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if !current.Writable {
		writeError(w, r, http.StatusForbidden, errNetworkNotEnabled(current.Why))
		return
	}
	body.Connection = current.Connection

	s.log.Warn("changing network settings", "connection", body.Connection,
		"dhcp", body.DHCP, "address", body.Address, "from", r.RemoteAddr)
	if err := netconf.Apply(r.Context(), body, NetworkRevertAfter); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                   true,
		"note":                 t(r, "note.network.applied", NetworkRevertAfter),
		"revert_after_seconds": int(NetworkRevertAfter.Seconds()),
	})
}

// handleNetworkConfirm cancels the armed revert.
func (s *Server) handleNetworkConfirm(w http.ResponseWriter, r *http.Request) {
	if err := sameSite(r); err != nil {
		writeError(w, r, http.StatusForbidden, err)
		return
	}
	if err := netconf.Confirm(r.Context()); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	s.log.Info("network settings confirmed", "from", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
