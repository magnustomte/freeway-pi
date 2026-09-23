package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"freewaypi/internal/bus"
	"freewaypi/internal/modbus"
)

// DefaultPort is the Modbus TCP port. Clients configured for Freeway WEB use
// it, so the replacement has to as well.
const DefaultPort = 502

// MaxClients bounds how many connections the gateway will hold at once.
//
// Generous: the one client this exists for opens a single connection and keeps
// it. The ceiling is there so a client that opens and abandons connections
// cannot use up the daemon's file descriptors.
const MaxClients = 64

// IdleTimeout is how long a connection may say nothing before it is closed.
//
// A Modbus client holds its connection open indefinitely between requests —
// the Homey app sets no idle timeout at all — so this has to be long enough to
// be about a peer that has gone away rather than one that is merely quiet.
const IdleTimeout = 10 * time.Minute

// DefaultRequestTimeout bounds one request's time on the bus. It sits below
// the five seconds the Homey app allows, so that a client times out because
// the unit is slow rather than because this gateway held on too long.
const DefaultRequestTimeout = 4 * time.Second

// ClientInfo describes one connected Modbus client, for the system page.
type ClientInfo struct {
	ID          uint64    `json:"id"`
	Addr        string    `json:"addr"`
	Connected   time.Time `json:"connected"`
	LastRequest time.Time `json:"last_request"`
	Requests    uint64    `json:"requests"`
	Errors      uint64    `json:"errors"`
	// LastFunction is the most recent function code, which makes it obvious at
	// a glance whether a client is only reading.
	LastFunction byte `json:"last_function"`
	LastUnit     byte `json:"last_unit"`
	// Name is the reverse-DNS name, when the network has one. "homey" says
	// more than an address does, and an address that has moved says nothing
	// at all.
	Name string `json:"name,omitempty"`
}

// Label is what to call this client: its name if the network knows one, and
// its address otherwise.
func (c ClientInfo) Label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Host()
}

// Host is the address without the port, which is the part that identifies the
// machine rather than this one connection.
func (c ClientInfo) Host() string {
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return c.Addr
	}
	return unmap(host)
}

type clientEntry struct {
	mu   sync.Mutex
	info ClientInfo
}

// Server answers Modbus TCP on behalf of the unit.
type Server struct {
	bus     *bus.Bus
	acl     *ACL
	breaker *Breaker
	log     *slog.Logger
	timeout time.Duration

	mu      sync.Mutex
	clients map[uint64]*clientEntry
	// conns is kept so shutdown can close them. A Modbus client holds its
	// connection open indefinitely — the Homey app sets no idle timeout at
	// all — so waiting for the handlers to finish means waiting forever, and
	// systemd kills the service after its timeout instead.
	conns   map[net.Conn]struct{}
	nextID  uint64
	refused uint64
	served  uint64

	names *resolver
	ln    net.Listener
	// stop ends the current listener's accept loop; nil when not listening.
	// Separate from closed, which ends the server for good: the switch on the
	// System page stops and starts the listener many times over one server.
	stop   chan struct{}
	addr   string
	wg     sync.WaitGroup
	closed chan struct{}
	once   sync.Once
}

// Options configures a Server.
type Options struct {
	ACL            *ACL
	Breaker        *Breaker
	Logger         *slog.Logger
	RequestTimeout time.Duration
}

// New returns a server that is not yet listening.
func New(b *bus.Bus, o Options) *Server {
	if o.ACL == nil {
		o.ACL = NewACL()
	}
	if o.Breaker == nil {
		o.Breaker = NewBreaker(BreakerOptions{})
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = DefaultRequestTimeout
	}
	return &Server{
		bus:     b,
		acl:     o.ACL,
		breaker: o.Breaker,
		log:     o.Logger,
		timeout: o.RequestTimeout,
		clients: make(map[uint64]*clientEntry),
		conns:   make(map[net.Conn]struct{}),
		names:   newResolver(),
		closed:  make(chan struct{}),
	}
}

// Serve accepts connections on ln until Close is called.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	return s.accept(ln, nil)
}

// accept runs the accept loop. An error after closed or stop is the listener
// being shut on purpose and ends the loop quietly.
func (s *Server) accept(ln net.Listener, stop <-chan struct{}) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return nil
			case <-stop:
				return nil
			default:
			}
			// A single failed accept is not a reason to stop serving everyone.
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

// Configure applies the switch and the access list together.
//
// The list takes effect at once, and the listener starts or stops to match, so
// turning the gateway on from the System page works without a restart — and a
// box that started with it off can have it turned on at all. Off means the port
// is closed rather than open and refusing: nothing listens that has nothing to
// serve.
func (s *Server) Configure(enabled bool, allow []string, addr string) error {
	if err := s.acl.Set(enabled, allow); err != nil {
		return err
	}
	if !enabled {
		s.stopListening()
		return nil
	}
	return s.startListening(addr)
}

// Listening reports whether the gateway is accepting connections.
func (s *Server) Listening() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stop != nil
}

// Allow returns the access list as it is being applied.
func (s *Server) Allow() []string {
	entries := s.acl.Entries()
	out := make([]string, 0, len(entries))
	for _, p := range entries {
		out = append(out, p.String())
	}
	return out
}

func (s *Server) startListening(addr string) error {
	s.mu.Lock()
	if s.stop != nil && s.addr == addr {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	// A different address is a stop and a start; the same one is left alone,
	// so saving the access list does not drop every connected client.
	s.stopListening()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("gateway: listen on %s: %w", addr, err)
	}
	stop := make(chan struct{})
	s.mu.Lock()
	s.ln, s.stop, s.addr = ln, stop, addr
	s.mu.Unlock()
	s.log.Info("modbus gateway listening", "addr", ln.Addr().String(), "allow", s.Allow())

	go func() {
		if err := s.accept(ln, stop); err != nil && !errors.Is(err, net.ErrClosed) {
			s.log.Error("modbus gateway stopped", "err", err)
		}
	}()
	return nil
}

// stopListening closes the listener and every client on it. A client left
// connected to a gateway that has been switched off would go on being served,
// which is the opposite of what the switch says.
func (s *Server) stopListening() {
	s.mu.Lock()
	ln, stop := s.ln, s.stop
	s.ln, s.stop, s.addr = nil, nil, ""
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	_ = ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	s.log.Info("modbus gateway stopped listening")
}

// ListenAndServe binds addr and serves it.
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("gateway: listen on %s: %w", addr, err)
	}
	return s.Serve(ln)
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()

	s.mu.Lock()
	if len(s.conns) >= MaxClients {
		s.refused++
		s.mu.Unlock()
		// Dropped without a word. There is no Modbus way to say "too many",
		// and the one client this exists for uses a single connection: being
		// at the ceiling means something is opening and abandoning them.
		s.log.Warn("modbus connection refused: too many open",
			"peer", conn.RemoteAddr().String(), "limit", MaxClients)
		return
	}
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()

	peer, _ := netip.ParseAddrPort(conn.RemoteAddr().String())
	if !s.acl.Allows(peer.Addr()) {
		s.mu.Lock()
		s.refused++
		s.mu.Unlock()
		// Logged at info, not warn: on a home network this is usually a port
		// scanner or a forgotten client, not an attack, but it is exactly what
		// someone debugging "why will Homey not connect" needs to see.
		s.log.Info("modbus client refused", "peer", peer.Addr().String(), "enabled", s.acl.Enabled())
		return
	}

	// The Homey app keeps its connection open indefinitely and sets no idle
	// timeout, so neither do we. Keepalive is what reclaims a peer that
	// vanished without closing.
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	}

	entry := s.register(peer)
	defer s.deregister(entry.info.ID)
	s.log.Info("modbus client connected", "peer", peer.String(), "id", entry.info.ID)

	for {
		// A peer that connects and then says nothing would otherwise hold a
		// goroutine and a file descriptor for as long as its TCP session
		// survived — which, with keepalive doing the only reaping, can be
		// hours. Long enough that a client which is merely quiet between
		// requests is never cut off.
		_ = conn.SetReadDeadline(time.Now().Add(IdleTimeout))
		frame, err := modbus.ReadTCPFrame(conn)
		if err != nil {
			if err != io.EOF {
				s.log.Info("modbus client gone", "peer", peer.String(), "id", entry.info.ID, "reason", err)
			}
			return
		}

		// No deadline while the bus does the work: a request can wait behind a
		// poll, and that is not the client being slow.
		_ = conn.SetReadDeadline(time.Time{})
		resp := s.execute(frame, entry)
		if _, err := conn.Write(resp.Encode()); err != nil {
			return
		}
	}
}

// execute answers one request.
func (s *Server) execute(req *modbus.TCPFrame, entry *clientEntry) *modbus.TCPFrame {
	reply := func(pdu []byte, failed bool) *modbus.TCPFrame {
		entry.note(req, failed)
		s.mu.Lock()
		s.served++
		s.mu.Unlock()
		return &modbus.TCPFrame{Transaction: req.Transaction, Unit: req.Unit, PDU: pdu}
	}

	if len(req.PDU) == 0 {
		return reply(modbus.ExceptionPDU(0, modbus.ExceptionIllegalFunction), true)
	}
	fc := req.PDU[0]

	// A function this gateway cannot frame gets the protocol's own answer
	// rather than a dropped connection.
	if _, err := modbus.RequestQuantity(req.PDU); err != nil {
		return reply(modbus.ExceptionPDU(fc, modbus.ExceptionIllegalFunction), true)
	}

	if !s.breaker.Allow() {
		return reply(modbus.ExceptionPDU(fc, modbus.ExceptionGatewayTargetNoResponse), true)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	var out []byte
	err := s.bus.Do(ctx, bus.PriorityClient, func(c *modbus.Client) error {
		pdu, err := c.ExecutePDU(req.Unit, req.PDU)
		if err != nil {
			return err
		}
		out = pdu
		return nil
	})
	if err != nil {
		s.breaker.Failure()
		s.log.Debug("modbus request failed", "unit", req.Unit, "function", modbus.FunctionName(fc), "err", err)
		return reply(modbus.ExceptionPDU(fc, modbus.ExceptionGatewayTargetNoResponse), true)
	}
	s.breaker.Success()

	// An exception from the unit is the unit's own answer and goes back
	// untouched. A client asking for a register this unit lacks must see the
	// same illegal data address it has always seen.
	return reply(out, len(out) > 0 && out[0]&0x80 != 0)
}

func (s *Server) register(peer netip.AddrPort) *clientEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	e := &clientEntry{info: ClientInfo{
		ID:        s.nextID,
		Addr:      peer.String(),
		Connected: time.Now(),
	}}
	s.clients[e.info.ID] = e
	// Starts the reverse lookup without waiting for it, so a name is usually
	// ready by the time anyone opens a page. The connection is never delayed:
	// Name answers from cache and resolves behind it.
	s.names.Name(peer.Addr().Unmap().String())
	return e
}

func (s *Server) deregister(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, id)
}

func (e *clientEntry) note(req *modbus.TCPFrame, failed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.info.Requests++
	if failed {
		e.info.Errors++
	}
	e.info.LastRequest = time.Now()
	if len(req.PDU) > 0 {
		e.info.LastFunction = req.PDU[0]
	}
	e.info.LastUnit = req.Unit
}

// Clients returns the currently connected clients. This is what answers "is
// anything actually talking to the gateway", which the old interface could not.
func (s *Server) Clients() []ClientInfo {
	s.mu.Lock()
	entries := make([]*clientEntry, 0, len(s.clients))
	for _, e := range s.clients {
		entries = append(entries, e)
	}
	s.mu.Unlock()

	out := make([]ClientInfo, 0, len(entries))
	for _, e := range entries {
		e.mu.Lock()
		info := e.info
		e.mu.Unlock()
		// Looked up here rather than on connect: a lookup that fails is
		// retried the next time somebody looks at the page, and a connection
		// is never delayed by DNS.
		info.Name = s.names.describe(info.Addr)
		out = append(out, info)
	}
	return out
}

// ServerStats summarises the gateway.
type ServerStats struct {
	Connected int          `json:"connected"`
	Served    uint64       `json:"served"`
	Refused   uint64       `json:"refused"`
	Breaker   BreakerState `json:"breaker"`
}

// Stats returns the gateway's counters.
func (s *Server) Stats() ServerStats {
	s.mu.Lock()
	st := ServerStats{Connected: len(s.clients), Served: s.served, Refused: s.refused}
	s.mu.Unlock()
	st.Breaker = s.breaker.State()
	return st
}

// Close stops accepting and hangs up on the clients.
//
// Closing them rather than waiting for them: a Modbus client keeps its
// connection open for as long as it runs, so waiting would mean waiting until
// somebody turns the client off. Its own reconnection is what puts it back,
// which the Homey app does within five seconds.
func (s *Server) Close() error {
	s.once.Do(func() { close(s.closed) })

	s.mu.Lock()
	ln := s.ln
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
	return nil
}
