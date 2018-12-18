package goftp

import (
	"bufio"
	"log"
	"net"
)


type Dialer interface {
	Dial(network, address string) (net.Conn, error)
}

func ConnectWithDialerDbg(addr string, dialer Dialer) (*FTP, error) {
	return connect(addr, dialer, true)
}

func ConnectWithDialer(addr string, dialer Dialer) (*FTP, error) {
	return connect(addr, dialer, false)
}

// Connect to server at addr (format "host:port"). debug is OFF
func Connect(addr string) (*FTP, error) {
	return connect(addr, &net.Dialer{}, true)
}

// ConnectDbg to server at addr (format "host:port"). debug is ON
func ConnectDbg(addr string) (*FTP, error) {
	return connect(addr, &net.Dialer{}, true)
}

func connect(addr string, dialer Dialer, dbg bool) (*FTP, error) {
	var err error
	var conn net.Conn

	if conn, err = dialer.Dial("tcp", addr); err != nil {
		return nil, err
	}

	writer := bufio.NewWriter(conn)
	reader := bufio.NewReader(conn)

	var line string

	object := &FTP{conn: conn, addr: addr, reader: reader, writer: writer, debug: dbg, dialer: dialer}
	line, _ = object.receive()

	if dbg {
		log.Print(line)
	}

	return object, nil
}
