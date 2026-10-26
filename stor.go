package goftp

import (
	"io"
	"net"
	"strings"
	"time"
)

// Stor uploads file to remote host path, from r
func (ftp *FTP) Stor(path string, r io.Reader) (err error) {
	start := time.Now()
	var written int64
	defer func() { ftp.observe(Event{Phase: "store", Command: "STOR", Bytes: written}, start, err) }()
	if err = ftp.Type(TypeImage); err != nil {
		return
	}

	var port int
	if port, err = ftp.Pasv(); err != nil {
		return
	}

	if err = ftp.send("STOR %s", path); err != nil {
		return
	}

	var pconn net.Conn
	if pconn, err = ftp.newConnection(port); err != nil {
		return
	}
	defer pconn.Close()

	var line string
	if line, err = ftp.receive(); err != nil {
		return
	}

	statusCode := StatusCode(line)
	if !(statusCode == StatusFileOK || statusCode == StatusAlreadyOpen) {
		err = &replyError{line: line}
		return
	}

	copyStart := time.Now()
	written, err = io.Copy(pconn, r)
	ftp.observe(Event{Phase: "data_copy", Command: "STOR", Bytes: written}, copyStart, err)
	if err != nil {
		return
	}
	if err = pconn.Close(); err != nil {
		return
	}

	if line, err = ftp.receive(); err != nil {
		return
	}

	if !strings.HasPrefix(line, StatusClosingDataConnection) {
		err = &replyError{line: line}
		return
	}

	return

}
