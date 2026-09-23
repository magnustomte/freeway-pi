// Package serial opens and configures an RS-485 adapter's tty and reads from it
// with real deadlines.
//
// Go's os.File cannot set read deadlines on a character device, so the port is
// opened non-blocking and every read goes through poll(2). That is also what
// makes the turnaround measurements in fwprobe meaningful: the time recorded is
// the unit's, not the kernel's buffering.
package serial

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Parity settings. Modbus RTU's default is even parity with one stop bit; a
// unit configured for no parity should use two stop bits, though most accept
// one. The EDA unit runs 19200 8N1 in practice.
type Parity int

const (
	ParityNone Parity = iota
	ParityEven
	ParityOdd
)

// Config describes the line settings.
type Config struct {
	Path     string
	Baud     int
	DataBits int // 7 or 8
	Parity   Parity
	StopBits int // 1 or 2
	// ReadTimeout is only a backstop; every read passes its own deadline.
	ReadTimeout time.Duration
}

// DefaultConfig returns the settings the EDA board uses on the Freeway
// connector: 19200 bps, 8 data bits, no parity.
func DefaultConfig(path string) Config {
	return Config{Path: path, Baud: 19200, DataBits: 8, Parity: ParityNone, StopBits: 1}
}

// Port is an open serial port. It is not safe for concurrent use.
type Port struct {
	// fd is closedFD when there is no port open. Not zero: zero is standard
	// input, and a failed reopen used to leave it there — so every write went
	// to stdin and every read came back empty, which under systemd is
	// /dev/null quietly accepting everything.
	fd   int
	cfg  Config
	file *os.File // kept only so the fd has an owner and a finalizer
}

// closedFD marks a port that is not open. -1 is what every syscall here
// rejects, so a use after a failed open fails loudly instead of writing
// somewhere else.
const closedFD = -1

// Closed returns a port that is not open yet, for a box whose adapter is not
// plugged in.
//
// The daemon used to refuse to start without the serial port, which on a
// freshly flashed box means the web interface never comes up — and the page
// that would explain why is the one that will not load. Losing the bus while
// running is already handled: the reopen loop, exception 11 to Modbus clients,
// and the strip across the top of every page. Starting without it is the same
// state, so it is the same code.
func Closed(cfg Config) *Port {
	return &Port{fd: closedFD, cfg: cfg}
}

// ErrClosed is returned by every operation on a port that is not open.
var ErrClosed = errors.New("serial: the port is not open")

func (p *Port) ok() error {
	if p.fd == closedFD || p.file == nil {
		return ErrClosed
	}
	return nil
}

// Reopen closes the port and opens it again on the same settings.
//
// For the adapter that was unplugged and put back. The descriptor a removed
// USB device leaves behind never recovers — every read returns an error for as
// long as the process lives — and this appliance is meant to go years without
// anybody logging in to restart it.
//
// The caller must not be reading or writing at the time. The bus owns the port
// and runs one job at a time, which is what makes that true here.
func (p *Port) Reopen() error {
	if p.file != nil {
		_ = p.file.Close()
	}
	p.fd, p.file = closedFD, nil

	fresh, err := Open(p.cfg)
	if err != nil {
		return err
	}
	p.fd, p.file = fresh.fd, fresh.file
	return nil
}

var baudRates = map[int]uint32{
	1200: unix.B1200, 2400: unix.B2400, 4800: unix.B4800, 9600: unix.B9600,
	19200: unix.B19200, 38400: unix.B38400, 57600: unix.B57600, 115200: unix.B115200,
}

// Open configures the port and returns it ready for use.
func Open(cfg Config) (*Port, error) {
	speed, ok := baudRates[cfg.Baud]
	if !ok {
		return nil, fmt.Errorf("serial: unsupported baud rate %d", cfg.Baud)
	}
	if cfg.DataBits == 0 {
		cfg.DataBits = 8
	}
	if cfg.StopBits == 0 {
		cfg.StopBits = 1
	}

	fd, err := unix.Open(cfg.Path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("serial: open %s: %w", cfg.Path, err)
	}

	t := unix.Termios{}
	// Raw mode: no canonical processing, no echo, no signal characters, no
	// translation of any byte in either direction.
	t.Cflag = unix.CREAD | unix.CLOCAL | speed
	switch cfg.DataBits {
	case 7:
		t.Cflag |= unix.CS7
	case 8:
		t.Cflag |= unix.CS8
	default:
		unix.Close(fd)
		return nil, fmt.Errorf("serial: unsupported data bits %d", cfg.DataBits)
	}
	switch cfg.Parity {
	case ParityNone:
	case ParityEven:
		t.Cflag |= unix.PARENB
	case ParityOdd:
		t.Cflag |= unix.PARENB | unix.PARODD
	}
	if cfg.StopBits == 2 {
		t.Cflag |= unix.CSTOPB
	}
	t.Iflag = unix.IGNPAR
	t.Oflag = 0
	t.Lflag = 0
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 0
	t.Ispeed = speed
	t.Ospeed = speed

	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &t); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("serial: configure %s: %w", cfg.Path, err)
	}

	p := &Port{fd: fd, cfg: cfg, file: os.NewFile(uintptr(fd), cfg.Path)}
	_ = p.Drain()
	return p, nil
}

// Close releases the port.
func (p *Port) Close() error {
	if p.fd < 0 {
		return nil
	}
	fd := p.fd
	p.fd = -1
	return unix.Close(fd)
}

// Drain discards anything already received. Called before every request so a
// late answer to a previous one cannot be read as this one's.
func (p *Port) Drain() error {
	if err := p.ok(); err != nil {
		return err
	}
	return unix.IoctlSetInt(p.fd, unix.TCFLSH, unix.TCIFLUSH)
}

// Write sends the whole buffer and waits until the last bit has left the
// adapter, so the caller's clock starts when the unit's turn begins.
func (p *Port) Write(b []byte) error {
	if err := p.ok(); err != nil {
		return err
	}
	for len(b) > 0 {
		n, err := unix.Write(p.fd, b)
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("serial: write: %w", err)
		}
		b = b[n:]
	}
	// tcdrain(3): on Linux, TCSBRK with a non-zero argument.
	if err := unix.IoctlSetInt(p.fd, unix.TCSBRK, 1); err != nil {
		return fmt.Errorf("serial: drain output: %w", err)
	}
	return nil
}

// ReadFull fills buf or returns os.ErrDeadlineExceeded.
func (p *Port) ReadFull(buf []byte, deadline time.Time) error {
	if err := p.ok(); err != nil {
		return err
	}
	got := 0
	for got < len(buf) {
		remain := time.Until(deadline)
		if remain <= 0 {
			return os.ErrDeadlineExceeded
		}
		ms := int(remain / time.Millisecond)
		if ms < 1 {
			ms = 1
		}
		fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, ms)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("serial: poll: %w", err)
		}
		if n == 0 {
			return os.ErrDeadlineExceeded
		}
		n, err = unix.Read(p.fd, buf[got:])
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("serial: read: %w", err)
		}
		got += n
	}
	return nil
}
