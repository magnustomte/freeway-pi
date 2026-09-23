package web

import (
	"net/http"

	"freewaypi/internal/netconf"
)

// The wifi controls.
//
// Freeway Pi prefers a cable — see the README — but a ventilation unit does not
// always stand where an ethernet socket does, and "prefers" is not a reason to
// leave somebody with no way to set the box up. So: wifi works, and this is
// where it is configured after the fact. At flashing time Raspberry Pi Imager
// has already asked for the network, which is why nothing here runs at install.

func (s *Server) handleWiFiGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"wifi":                 netconf.ReadWiFi(r.Context()),
		"pending":              netconf.Pending(r.Context()),
		"revert_after_seconds": int(netconf.WiFiRevertAfter.Seconds()),
	})
}

// handleWiFiScan asks the radio to look now, which takes a few seconds. A POST
// rather than a GET because it makes the hardware do something.
func (s *Server) handleWiFiScan(w http.ResponseWriter, r *http.Request) {
	if err := sameSite(r); err != nil {
		writeError(w, r, http.StatusForbidden, err)
		return
	}
	networks, err := netconf.Scan(r.Context())
	if err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"networks": networks})
}

// handleWiFiJoin connects to a network.
//
// The passphrase arrives here, goes straight to the root helper and is never
// stored or logged — the log line below deliberately names the network and not
// what was typed into the box beside it.
func (s *Server) handleWiFiJoin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SSID string `json:"ssid"`
		PSK  string `json:"psk"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	state := netconf.ReadWiFi(r.Context())
	if !state.Supported {
		writeError(w, r, http.StatusConflict, errNoWiFi())
		return
	}
	if !state.Writable {
		writeError(w, r, http.StatusForbidden, errNetworkNotEnabled(state.Why))
		return
	}

	s.log.Warn("joining a wifi network", "ssid", body.SSID, "from", r.RemoteAddr)
	if err := netconf.Join(r.Context(), body.SSID, body.PSK, netconf.WiFiRevertAfter); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	// A revert is only armed when the box was already on wifi, because that is
	// the only way this change can take the box away. Say which happened, so
	// nobody sits waiting to confirm something that needs no confirming.
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                   true,
		"needs_confirm":        state.Connected,
		"note":                 t(r, "note.wifi.joined", netconf.WiFiRevertAfter),
		"revert_after_seconds": int(netconf.WiFiRevertAfter.Seconds()),
	})
}

func (s *Server) handleWiFiForget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SSID string `json:"ssid"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.log.Warn("forgetting a wifi network", "ssid", body.SSID, "from", r.RemoteAddr)
	if err := netconf.Forget(r.Context(), body.SSID); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleWiFiCountry sets the regulatory domain, without which the radio stays
// blocked and a scan finds nothing at all.
func (s *Server) handleWiFiCountry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Country string `json:"country"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := netconf.SetCountry(r.Context(), body.Country); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	s.log.Info("wifi country set", "country", body.Country, "from", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleWiFiRadio turns the radio on. See netconf.EnableRadio for why this is
// worth a button of its own.
func (s *Server) handleWiFiRadio(w http.ResponseWriter, r *http.Request) {
	if err := sameSite(r); err != nil {
		writeError(w, r, http.StatusForbidden, err)
		return
	}
	if err := netconf.EnableRadio(r.Context()); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	s.log.Info("wifi radio switched on", "from", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// The ssh controls.
//
// Closing ssh is the one setting here that can leave somebody with no way into
// their own box, so the interface states the cost of each choice rather than
// presenting three equal buttons.

func (s *Server) handleSSHGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ssh": netconf.ReadSSH(r.Context())})
}

func (s *Server) handleSSHPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Policy  string `json:"policy"`
		Minutes int    `json:"minutes"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}

	var err error
	switch {
	case body.Minutes > 0:
		s.log.Warn("opening ssh for a while", "minutes", body.Minutes, "from", r.RemoteAddr)
		err = netconf.OpenSSH(r.Context(), body.Minutes)
	case body.Policy == "":
		s.log.Info("ending the temporary ssh opening", "from", r.RemoteAddr)
		err = netconf.ShutSSH(r.Context())
	default:
		s.log.Warn("changing the lasting ssh setting", "policy", body.Policy, "from", r.RemoteAddr)
		err = netconf.SetSSHPolicy(r.Context(), body.Policy)
	}
	if err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ssh": netconf.ReadSSH(r.Context())})
}

// handleSSHKeys adds or removes a key that may log in.
func (s *Server) handleSSHKeys(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key         string `json:"key"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	var err error
	if body.Fingerprint != "" {
		s.log.Warn("removing an ssh key", "fingerprint", body.Fingerprint, "from", r.RemoteAddr)
		err = netconf.RemoveSSHKey(r.Context(), body.Fingerprint)
	} else {
		s.log.Warn("adding an ssh key", "from", r.RemoteAddr)
		err = netconf.AddSSHKey(r.Context(), body.Key)
	}
	if err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ssh": netconf.ReadSSH(r.Context())})
}
