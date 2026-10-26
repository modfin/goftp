package goftp

import (
	"errors"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

// Event describes a completed FTP operation. Time is UTC and Duration is elapsed
// time. Command contains only a known command name, never its arguments. Raw
// replies, file names, credentials and payloads are deliberately excluded.
type Event struct {
	Time        time.Time     `json:"time"`
	Phase       string        `json:"phase"`
	Command     string        `json:"command,omitempty"`
	ReplyCode   int           `json:"reply_code,omitempty"`
	PassivePort int           `json:"passive_port,omitempty"`
	Duration    time.Duration `json:"duration"`
	Bytes       int64         `json:"bytes,omitempty"`
	ErrorKind   string        `json:"error_kind,omitempty"`
	CloseReason string        `json:"close_reason,omitempty"`
}

// Observer receives events synchronously, serialized per connection. A PASV
// event may be delivered from a worker goroutine. Observers must return promptly
// and must not call methods on the FTP connection. An observer shared by several
// connections must provide its own synchronization.
type Observer func(Event)

func (ftp *FTP) observe(event Event, start time.Time, err error) {
	if ftp.observer == nil && !ftp.debug {
		return
	}
	event.Time = time.Now().UTC()
	event.Duration = time.Since(start)
	event.ErrorKind = errorKind(err)
	if event.ErrorKind == "" && event.ReplyCode >= 400 {
		event.ErrorKind = "server_reply"
	}
	ftp.observerMu.Lock()
	defer ftp.observerMu.Unlock()
	if ftp.debug {
		log.Printf("FTP time=%s phase=%s command=%s reply_code=%d passive_port=%d duration=%s bytes=%d error_kind=%s close_reason=%s",
			event.Time.Format(time.RFC3339Nano), event.Phase, event.Command, event.ReplyCode,
			event.PassivePort, event.Duration, event.Bytes, event.ErrorKind, event.CloseReason)
	}
	if ftp.observer != nil {
		ftp.observer(event)
	}
}

func errorKind(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrPasvTimeout) {
		return "pasv_timeout"
	}
	if errors.Is(err, ErrPasvBadAnswer) {
		return "invalid_pasv_reply"
	}
	var reply *replyError
	if errors.As(err, &reply) {
		return "server_reply"
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "timeout"
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
		return "closed"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "eof"
	}
	return "error"
}

// Retain existing error text for callers without exposing server-controlled
// text (which can echo credentials or file names) to the observer.
type replyError struct{ line string }

func (err *replyError) Error() string { return err.line }

func replyCode(line string) int {
	code, _ := strconv.Atoi(StatusCode(line))
	return code
}

func commandName(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "UNKNOWN"
	}
	name := strings.ToUpper(fields[0])
	switch name {
	case "USER", "PASS", "ACCT", "AUTH", "PBSZ", "PROT", "CCC",
		"CWD", "CDUP", "SMNT", "QUIT", "REIN", "PORT", "PASV", "EPRT", "EPSV",
		"TYPE", "STRU", "MODE", "RETR", "STOR", "STOU", "APPE", "ALLO", "REST",
		"RNFR", "RNTO", "ABOR", "DELE", "RMD", "MKD", "PWD", "LIST", "NLST",
		"SITE", "SYST", "STAT", "HELP", "NOOP", "SIZE", "MDTM", "MLSD", "MLST", "FEAT", "OPTS":
		return name
	default:
		return "UNKNOWN"
	}
}
