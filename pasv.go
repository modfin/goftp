package goftp

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultPasvTimeout bounds the PASV command/reply exchange, not the upload.
const DefaultPasvTimeout = 10 * time.Second

// ErrPasvTimeout means the PASV exchange timed out and the control connection
// was closed. Establish a new connection before retrying. Its text is retained
// for compatibility; it also implements net.Error.
var ErrPasvTimeout = &pasvTimeoutError{}

// ErrPasvBadAnswer means the server returned an invalid passive port.
var ErrPasvBadAnswer = errors.New("PasvBadAnswer")

type pasvTimeoutError struct{}

func (*pasvTimeoutError) Error() string   { return "PasvTimeout" }
func (*pasvTimeoutError) Timeout() bool   { return true }
func (*pasvTimeoutError) Temporary() bool { return false }

var passiveAddress = regexp.MustCompile(`\((.*)\)`)

type pasvResult struct {
	port int
	err  error
}

// Pasv enables a passive data connection and returns its port number.
func (ftp *FTP) Pasv() (port int, err error) {
	start := time.Now()
	defer func() { ftp.observe(Event{Phase: "pasv", Command: "PASV", PassivePort: port}, start, err) }()
	timeout := ftp.pasvTimeout
	if timeout == 0 {
		timeout = DefaultPasvTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	// The worker owns its results. It cannot overwrite the timeout error when
	// closing the connection unblocks receive().
	done := make(chan pasvResult, 1)
	go func() {
		port, err := ftp.pasv()
		done <- pasvResult{port: port, err: err}
	}()
	select {
	case result := <-done:
		return result.port, result.err
	case <-timer.C:
		ftp.closeWithReason("pasv_timeout")
		// net.Conn.Close unblocks pending I/O. Join the worker before returning
		// so it cannot access the reader or emit events after the next call.
		<-done
		return 0, ErrPasvTimeout
	}
}

func (ftp *FTP) pasv() (int, error) {
	line, err := ftp.cmd("227", "PASV")
	if err != nil {
		return 0, err
	}
	res := passiveAddress.FindStringSubmatch(line)
	if len(res) < 2 {
		return 0, ErrPasvBadAnswer
	}
	parts := strings.Split(res[1], ",")
	if len(parts) < 2 {
		return 0, ErrPasvBadAnswer
	}
	high, highErr := strconv.Atoi(strings.TrimSpace(parts[len(parts)-2]))
	low, lowErr := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1]))
	if highErr != nil || lowErr != nil || high < 0 || high > 255 || low < 0 || low > 255 {
		return 0, ErrPasvBadAnswer
	}
	port := high<<8 | low
	if port == 0 {
		return 0, ErrPasvBadAnswer
	}
	return port, nil
}
