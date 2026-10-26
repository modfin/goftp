package goftp

import (
	"bufio"
	"crypto/tls"
	"time"
)

// AuthTLS secures the ftp connection by using TLS
func (ftp *FTP) AuthTLS(config *tls.Config) (err error) {
	start := time.Now()
	defer func() { ftp.observe(Event{Phase: "auth_tls"}, start, err) }()
	if _, err := ftp.cmd("234", "AUTH TLS"); err != nil {
		return err
	}

	// wrap tls on existing connection
	ftp.tlsconfig = config

	conn := tls.Client(ftp.conn, config)
	ftp.conn = conn
	ftp.writer = bufio.NewWriter(ftp.conn)
	ftp.reader = bufio.NewReader(ftp.conn)
	startHandshake := time.Now()
	err = conn.Handshake()
	ftp.observe(Event{Phase: "tls_handshake"}, startHandshake, err)
	if err != nil {
		ftp.closeWithReason("tls_error")
		return err
	}

	if _, err := ftp.cmd(StatusOK, "PBSZ 0"); err != nil {
		return err
	}

	if _, err := ftp.cmd(StatusOK, "PROT P"); err != nil {
		return err
	}

	return nil
}
