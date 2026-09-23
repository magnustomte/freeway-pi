// Package web serves the user interface and the API behind it.
//
// Everything this package exposes is what the old interface called daily use:
// reading the state, and changing temperature, fan level and mode. None of it
// is behind a PIN, deliberately — the household should be able to turn up the
// heat without knowing a code. Settings, timer programs and the system page
// come later and do sit behind one.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"freewaypi/internal/auth"
	"freewaypi/internal/bus"
	"freewaypi/internal/config"
	"freewaypi/internal/eda"
	"freewaypi/internal/gateway"
	"freewaypi/internal/i18n"
	"freewaypi/internal/notify"
)

//go:embed static
var staticFS embed.FS

// Controller is what the API is allowed to do to the unit.
type Controller interface {
	State() (*eda.State, error)
	Apply(ctx context.Context, writes []eda.Write) error
	SyncClock(ctx context.Context) error
	SetName(name string)
	Settings() ([]eda.SettingValue, error)
	Programs() ([]eda.WeekProgram, []eda.YearProgram, error)
	Alarms() ([]eda.Alarm, error)
}

// Server serves the interface.
type Server struct {
	ctrl     Controller
	poller   *bus.Poller
	history  History
	bus      *bus.Bus
	auth     *auth.Authenticator
	notifier *notify.Notifier
	gateway  *gateway.Server
	// power reports Freeway Pi's own supply, and may be nil where nothing is
	// watching it.
	power PowerSupply
	log   *slog.Logger

	// version, statePath and gatewayConfig are facts about this installation
	// that the system page reports and nothing else needs.
	version       string
	commit        string
	built         string
	statePath     string
	configPath    string
	gatewayConfig config.Gateway
	notifyConfig  config.Notify
	unitConfig    config.Unit
	updatesConfig config.Updates
	// saveConfig applies a change to the file on disk, reloading it first so
	// this process does not stamp its own idea of the rest over what is there.
	saveConfig func(func(*config.Config)) error
	mux        *http.ServeMux
}

// Options configures a Server.
type Options struct {
	Logger *slog.Logger
	// History may be nil, in which case the data section says it is not
	// recording rather than showing an empty chart.
	History History
	// Auth guards everything beyond daily use.
	Auth *auth.Authenticator
	// Notifier may be nil in tests; the server builds an empty one.
	Notifier *notify.Notifier
	// Power may be nil, in which case the interface says the supply is not
	// being watched rather than that it is fine.
	Power PowerSupply
	// Bus is reported on by the system page. Nil in tests.
	Bus *bus.Bus
	// Gateway is the Modbus server, for the client list. Nil when disabled.
	Gateway       *gateway.Server
	GatewayConfig config.Gateway
	NotifyConfig  config.Notify
	UnitConfig    config.Unit
	UpdatesConfig config.Updates
	Version       string
	Commit        string
	Built         string
	StatePath     string
	ConfigPath    string
	SaveConfig    func(func(*config.Config)) error
}

// New returns a server ready to be handed to http.Server.
func New(ctrl Controller, poller *bus.Poller, o Options) *Server {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Auth == nil {
		o.Auth = auth.New(auth.Config{})
	}
	if o.Notifier == nil {
		o.Notifier = notify.New(notify.Options{Logger: o.Logger})
	}
	if o.SaveConfig == nil {
		o.SaveConfig = func(func(*config.Config)) error {
			return i18n.Errf("error.config.readonly")
		}
	}
	if o.StatePath == "" {
		o.StatePath = "/"
	}
	s := &Server{
		ctrl: ctrl, poller: poller, history: o.History, auth: o.Auth,
		notifier: o.Notifier, gateway: o.Gateway, bus: o.Bus,
		power: o.Power, log: o.Logger,
		version: o.Version, commit: o.Commit, built: o.Built,
		statePath: o.StatePath, configPath: o.ConfigPath,
		gatewayConfig: o.GatewayConfig,
		notifyConfig:  o.NotifyConfig,
		unitConfig:    o.UnitConfig,
		updatesConfig: o.UpdatesConfig,
		saveConfig:    o.SaveConfig, mux: http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/state", s.handleState)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	// No PIN: this is the box saying it is being starved of power, and the
	// strip that shows it is on every page, including the ones a passer-by
	// sees. It says nothing about the house or the unit.
	s.mux.HandleFunc("GET /api/power", s.handleSupply)
	s.mux.HandleFunc("POST /api/control/mode", s.handleMode)
	s.mux.HandleFunc("POST /api/control/setpoint", s.handleSetpoint)
	s.mux.HandleFunc("POST /api/control/fan", s.handleFan)
	s.mux.HandleFunc("POST /api/control/overpressure", s.handleOverpressure)
	s.mux.HandleFunc("POST /api/control/boost", s.handleBoost)
	s.mux.HandleFunc("POST /api/control/clock", s.handleClock)
	s.mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("GET /api/auth/challenge", s.handleAuthChallenge)
	s.mux.HandleFunc("POST /api/auth/setup", s.handleAuthSetup)
	s.mux.HandleFunc("POST /api/auth/login", s.handleAuthLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleAuthLogout)
	s.mux.HandleFunc("POST /api/auth/change", s.requireAuth(s.handleAuthChange))

	s.mux.HandleFunc("GET /api/settings", s.requireAuth(s.handleSettings))
	s.mux.HandleFunc("PUT /api/settings", s.requireAuth(s.handleSetSetting))
	s.mux.HandleFunc("POST /api/settings/service-reset", s.requireAuth(s.handleServiceReset))
	s.mux.HandleFunc("GET /api/clock", s.requireAuth(s.handleClockGet))
	s.mux.HandleFunc("PUT /api/clock", s.requireAuth(s.handleClockPut))

	s.mux.HandleFunc("GET /api/programs", s.requireAuth(s.handleProgramsGet))
	s.mux.HandleFunc("PUT /api/programs/week", s.requireAuth(s.handleWeekProgramPut))
	s.mux.HandleFunc("PUT /api/programs/year", s.requireAuth(s.handleYearProgramPut))
	s.mux.HandleFunc("DELETE /api/programs/week", s.requireAuth(s.handleProgramDelete(true)))
	s.mux.HandleFunc("DELETE /api/programs/year", s.requireAuth(s.handleProgramDelete(false)))

	s.mux.HandleFunc("GET /api/alarms", s.requireAuth(s.handleAlarms))
	s.mux.HandleFunc("GET /api/system", s.requireAuth(s.handleSystem))
	s.mux.HandleFunc("GET /api/notifications", s.requireAuth(s.handleNotificationLog))
	s.mux.HandleFunc("PUT /api/system/gateway", s.requireAuth(s.handleGatewayPut))
	s.mux.HandleFunc("PUT /api/system/unit-name", s.requireAuth(s.handleUnitName))
	s.mux.HandleFunc("PUT /api/system/reboot-policy", s.requireAuth(s.handleRebootPolicy))
	s.mux.HandleFunc("POST /api/system/apt", s.requireAuth(s.handleApt))
	s.mux.HandleFunc("GET /api/system/apt", s.requireAuth(s.handleAptStatus))
	s.mux.HandleFunc("POST /api/system/reboot", s.requireAuth(s.handlePower("reboot")))
	s.mux.HandleFunc("POST /api/system/poweroff", s.requireAuth(s.handlePower("poweroff")))
	s.mux.HandleFunc("GET /api/network", s.requireAuth(s.handleNetworkGet))
	s.mux.HandleFunc("PUT /api/network", s.requireAuth(s.handleNetworkPut))
	s.mux.HandleFunc("POST /api/network/confirm", s.requireAuth(s.handleNetworkConfirm))
	s.mux.HandleFunc("GET /api/system/time", s.requireAuth(s.handleTimeGet))
	s.mux.HandleFunc("PUT /api/system/time", s.requireAuth(s.handleTimePut))
	s.mux.HandleFunc("GET /api/ssh", s.requireAuth(s.handleSSHGet))
	s.mux.HandleFunc("PUT /api/ssh", s.requireAuth(s.handleSSHPut))
	s.mux.HandleFunc("POST /api/ssh/keys", s.requireAuth(s.handleSSHKeys))
	s.mux.HandleFunc("GET /api/wifi", s.requireAuth(s.handleWiFiGet))
	s.mux.HandleFunc("POST /api/wifi/radio", s.requireAuth(s.handleWiFiRadio))
	s.mux.HandleFunc("POST /api/wifi/scan", s.requireAuth(s.handleWiFiScan))
	s.mux.HandleFunc("POST /api/wifi/join", s.requireAuth(s.handleWiFiJoin))
	s.mux.HandleFunc("POST /api/wifi/forget", s.requireAuth(s.handleWiFiForget))
	s.mux.HandleFunc("POST /api/wifi/country", s.requireAuth(s.handleWiFiCountry))

	s.mux.HandleFunc("GET /api/backup", s.requireAuth(s.handleBackup))
	s.mux.HandleFunc("GET /api/notify", s.requireAuth(s.handleNotifyGet))
	s.mux.HandleFunc("PUT /api/notify", s.requireAuth(s.handleNotifyPut))
	s.mux.HandleFunc("POST /api/notify/test", s.requireAuth(s.handleNotifyTest))

	s.mux.HandleFunc("GET /api/metrics", s.handleMetrics)
	s.mux.HandleFunc("GET /api/series", s.handleSeries)
	s.mux.HandleFunc("GET /api/series.csv", s.handleSeriesCSV)

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // the embedded directory is a build-time fact
	}
	s.mux.HandleFunc("GET /api/lang.js", s.handleCatalogueScript)
	s.mux.HandleFunc("GET /api/lang/{lang}", s.handleLanguages)

	s.mux.Handle("GET /", s.staticHandler(sub))
}

// staticHandler serves the interface, with a validator so a deploy takes effect
// on the next refresh.
//
// Everything here is embedded in the binary, and an embedded file's modification
// time is zero — so the file server sends no Last-Modified, and a browser with
// no validator at all is free to invent a lifetime for the file. The result is a
// refresh that fetches new markup and keeps the old stylesheet, which looks
// exactly like a layout bug and is not one.
//
// no-cache means "ask me first", not "do not keep a copy": the browser still
// caches, still revalidates in one round trip, and still gets a 304 for the
// unchanged case. Only the guessing goes away.
// staticTag is the validator for everything that ships inside the binary.
//
// A digest of the embedded files, not the version and commit. Those name a
// build; they do not identify its contents. A working tree that has been
// changed reports the same commit with the same "+endret" marker however many
// times it is built, so every deploy from a dirty tree produced a byte-for-byte
// identical validator — the browser revalidated exactly as asked, was told 304,
// and went on serving a stale interface that no amount of refreshing could
// shift. Observed: a card deployed and verified present in the file
// being served, and absent from the page.
//
// The same trap is waiting for anybody upgrading between two releases that
// forgot to change the version. Hashing what is actually served cannot get
// this wrong: the tag differs if and only if the bytes do.
func (s *Server) staticTag() string {
	staticTagOnce.Do(func() {
		sum := sha256.New()
		err := fs.WalkDir(staticFS, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := staticFS.ReadFile(path)
			if err != nil {
				return err
			}
			// The name goes in too, so moving a file between two paths is a
			// change even when no byte of any file differs.
			fmt.Fprintf(sum, "%s %d\n", path, len(b))
			sum.Write(b)
			return nil
		})
		if err != nil {
			// Embedded files cannot fail to read, but a validator that is
			// wrong is worse than none: fall back to something that changes
			// every restart rather than to something that never changes.
			staticTagValue = fmt.Sprintf("%q", time.Now().Format(time.RFC3339Nano))
			return
		}
		staticTagValue = fmt.Sprintf("%q", hex.EncodeToString(sum.Sum(nil))[:16])
	})
	return staticTagValue
}

// Computed once: the files are embedded and cannot change while the process
// runs, and hashing them on every request would be a waste on a Pi.
var (
	staticTagOnce  sync.Once
	staticTagValue string
)

func (s *Server) staticHandler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	// One tag for the whole interface. Every file in it ships inside this
	// binary, so any change to any of them is a different build — and the
	// commit already carries a marker when the tree was modified.
	tag := s.staticTag()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", tag)
		w.Header().Set("Cache-Control", "no-cache")
		// The 304 is left to the file server: it compares If-None-Match
		// against this header itself, and it still answers 404 for a path that
		// does not exist rather than "unchanged".
		files.ServeHTTP(w, r)
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The interface only ever runs on the local network, from the same origin,
	// so there is no cross-origin story to get wrong.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	s.mux.ServeHTTP(w, r)
}

// stateResponse is the unit's state plus the little this box knows that the
// overview shows beside it.
//
// The state is embedded rather than nested so the field names the page already
// reads stay where they were.
type stateResponse struct {
	*eda.State
	// Gateway reports whether the Modbus gateway is on and who is using it.
	// It belongs on the overview because a house being steered from somewhere
	// else is worth knowing before wondering why the fan changed by itself.
	Gateway gatewayState `json:"gateway"`
	// ActiveAlarms shadows the one inside eda.State, which carries codes and
	// no words. Go prefers the shallower field of the same name, so this is
	// what gets marshalled.
	ActiveAlarms []alarmView `json:"active_alarms"`
}

// gatewayState is the gateway as the overview shows it: is it on, and who is
// connected.
type gatewayState struct {
	Enabled bool          `json:"enabled"`
	Listen  string        `json:"listen,omitempty"`
	Clients []clientLabel `json:"clients"`
}

// clientLabel is one gateway client, named rather than numbered where the
// network has a name for it.
type clientLabel struct {
	// Label is the name where the network has one, and the address otherwise.
	// Host is always the address, so the interface can show both: a name is
	// what people recognise and an address is what they can act on.
	Label    string `json:"label"`
	Host     string `json:"host"`
	Addr     string `json:"addr"`
	Requests uint64 `json:"requests"`
}

func (s *Server) state(r *http.Request) (*stateResponse, error) {
	st, err := s.ctrl.State()
	if err != nil {
		return nil, err
	}
	out := &stateResponse{State: st}
	out.ActiveAlarms = s.alarmViews(r, st.ActiveAlarms)
	// This endpoint needs no PIN, by design: anybody in the house may change
	// the temperature. That bargain covers what the house is doing, not what
	// the unit's plate says, so the serial goes and the model stays — the
	// model is what an unnamed unit is called on screen. The PIN-gated system
	// page has the serial.
	out.Machine.Serial = 0
	s.localiseState(r, out)
	out.Gateway.Clients = []clientLabel{}
	if s.gateway != nil {
		// Live rather than the configuration read at startup: the switch on the
		// System page changes this without a restart.
		out.Gateway.Enabled = s.gateway.Listening()
		out.Gateway.Listen = s.gatewayConfig.Listen
		for _, c := range s.gateway.Clients() {
			out.Gateway.Clients = append(out.Gateway.Clients, clientLabel{
				Label: c.Label(), Addr: c.Addr, Host: c.Host(), Requests: c.Requests,
			})
		}
	}
	return out, nil
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	st, err := s.state(r)
	if err != nil {
		// Before the first poll completes there is nothing to show, and saying
		// so is better than an empty object that reads as zero degrees.
		writeError(w, r, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleEvents streams state to the page. The alternative, having every open
// tab poll, would put the interface's cost on the bus; this way the snapshot is
// read once and pushed to everyone.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	// Nothing here is proxied today, but a buffering proxy would turn this
	// stream into nothing arriving until it ends.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func() bool {
		st, err := s.state(r)
		if err != nil {
			return true // nothing to send yet; keep the connection
		}
		data, err := json.Marshal(st)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	send()

	// A tick rather than a subscription: the snapshot is an atomic pointer that
	// is cheap to read, and a second of extra latency on a value the unit only
	// refreshes every ten seconds is not worth a subscription mechanism.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var lastSent time.Time
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			st, err := s.ctrl.State()
			if err != nil {
				continue
			}
			if st.Taken.Equal(lastSent) {
				continue
			}
			lastSent = st.Taken
			if !send() {
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers with a failure, in the reader's language where the failure
// knows how to say itself in more than one.
func writeError(w http.ResponseWriter, r *http.Request, status int, err error) {
	writeJSON(w, status, map[string]string{"error": i18n.Translate(langOf(r), err)})
}
