package goftp

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"time"
)

type Dialer interface {
	Dial(network, address string) (net.Conn, error)
}

// Options configures a connection before dialing or reading the greeting.
type Options struct {
	// Dialer defaults to net.Dialer. It is also used for data connections.
	Dialer Dialer
	// PasvTimeout defaults to DefaultPasvTimeout when zero. Negative values
	// are invalid. This does not set a deadline for other FTP operations.
	PasvTimeout time.Duration
	Observer    Observer
	// Debug logs the same redacted fields as Observer, using the standard logger.
	Debug bool
}

func ConnectWithDialerDbg(addr string, dialer Dialer) (*FTP, error) {
	return ConnectWithOptions(addr, Options{Dialer: dialer, Debug: true})
}
func ConnectWithDialer(addr string, dialer Dialer) (*FTP, error) {
	return ConnectWithOptions(addr, Options{Dialer: dialer})
}

// Connect to server at addr (format "host:port"). debug is OFF
func Connect(addr string) (*FTP, error) { return ConnectWithOptions(addr, Options{}) }

// ConnectDbg to server at addr (format "host:port"). debug is ON
func ConnectDbg(addr string) (*FTP, error) { return ConnectWithOptions(addr, Options{Debug: true}) }

// ConnectWithOptions connects and reads the server greeting. Greeting read
// failures close the connection and are returned to the caller.
func ConnectWithOptions(addr string, options Options) (*FTP, error) {
	if options.PasvTimeout < 0 {
		return nil, errors.New("PasvTimeout must not be negative")
	}
	if options.PasvTimeout == 0 {
		options.PasvTimeout = DefaultPasvTimeout
	}
	if options.Dialer == nil {
		options.Dialer = &net.Dialer{}
	}
	ftp := &FTP{addr: addr, debug: options.Debug, dialer: options.Dialer, observer: options.Observer, pasvTimeout: options.PasvTimeout}
	start := time.Now()
	conn, err := ftp.dialer.Dial("tcp", addr)
	ftp.observe(Event{Phase: "dial"}, start, err)
	if err != nil {
		return nil, err
	}
	ftp.conn = conn
	ftp.writer = bufio.NewWriter(conn)
	ftp.reader = bufio.NewReader(conn)
	if _, err := ftp.receive(); err != nil {
		ftp.closeWithReason("greeting_error")
		return nil, fmt.Errorf("ftp greeting: %w", err)
	}
	return ftp, nil
}
