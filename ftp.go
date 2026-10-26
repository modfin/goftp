package goftp

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// RePwdPath is the default expression for matching files in the current working directory
var RePwdPath = regexp.MustCompile(`\"(.*)\"`)

// FTP is a session for File Transfer Protocol. Operations on a session must be
// performed serially; use separate sessions for concurrent transfers.
type FTP struct {
	dialer Dialer
	conn   net.Conn

	addr string

	debug     bool
	tlsconfig *tls.Config

	reader *bufio.Reader
	writer *bufio.Writer

	pasvTimeout time.Duration
	observer    Observer
	observerMu  sync.Mutex
	closeOnce   sync.Once
	closeErr    error

	command      string
	replyStarted time.Time
	storReplies  int
}

type (
	// WalkFunc is called on each path in a Walk. Errors are filtered through WalkFunc
	WalkFunc func(path string, info os.FileMode, err error) error

	// RetrFunc is passed to Retr and is the handler for the stream received for a given path
	RetrFunc func(r io.Reader) error
)

func parseLine(line string) (perm string, t string, filename string) {
	for _, v := range strings.Split(line, ";") {
		v2 := strings.Split(v, "=")

		switch v2[0] {
		case "perm":
			perm = v2[1]
		case "type":
			t = v2[1]
		default:
			filename = v[1 : len(v)-2]
		}
	}
	return
}

// private function to send command and compare return code with expects
func (ftp *FTP) cmd(expects string, command string, args ...interface{}) (line string, err error) {
	if err = ftp.send(command, args...); err != nil {
		return
	}

	if line, err = ftp.receive(); err != nil {
		return
	}

	if !strings.HasPrefix(line, expects) {
		err = &replyError{line: line}
		return
	}

	return
}

func (ftp *FTP) receiveLine() (string, error) {
	return ftp.reader.ReadString('\n')
}

func (ftp *FTP) receive() (string, error) {
	return ftp.receiveResponse(true)
}

func (ftp *FTP) receiveNoDiscard() (string, error) {
	return ftp.receiveResponse(false)
}

func (ftp *FTP) receiveResponse(discard bool) (line string, err error) {
	start := ftp.replyStarted
	if start.IsZero() {
		start = time.Now()
	}
	phase := "command_reply"
	if ftp.command == "" {
		phase = "greeting"
	} else if ftp.command == "STOR" {
		phase = "stor_preliminary"
		if ftp.storReplies > 0 {
			phase = "stor_completion"
		}
		ftp.storReplies++
	}
	defer func() {
		ftp.replyStarted = time.Time{}
		ftp.observe(Event{Phase: phase, Command: ftp.command, ReplyCode: replyCode(line)}, start, err)
	}()
	line, err = ftp.receiveLine()

	if err != nil {
		return line, err
	}

	if (len(line) >= 4) && (line[3] == '-') {
		//Multiline response
		closingCode := line[:3] + " "
		for {
			str, err := ftp.receiveLine()
			line = line + str
			if err != nil {
				return line, err
			}
			if len(str) < 4 {
				break
			} else {
				if str[:4] == closingCode {
					break
				}
			}
		}
	}
	if discard {
		ftp.ReadAndDiscard()
	}
	return line, err
}

func (ftp *FTP) send(command string, arguments ...interface{}) (err error) {
	command = fmt.Sprintf(command, arguments...)
	ftp.command = commandName(command)
	ftp.storReplies = 0
	ftp.replyStarted = time.Now()
	defer func() { ftp.observe(Event{Phase: "command_send", Command: ftp.command}, ftp.replyStarted, err) }()
	command += "\r\n"

	if _, err := ftp.writer.WriteString(command); err != nil {
		return err
	}

	if err := ftp.writer.Flush(); err != nil {
		return err
	}

	return nil
}

// open new data connection
func (ftp *FTP) newConnection(port int) (conn net.Conn, err error) {
	addr := fmt.Sprintf("%s:%d", strings.Split(ftp.addr, ":")[0], port)
	start := time.Now()
	defer func() { ftp.observe(Event{Phase: "data_dial", PassivePort: port}, start, err) }()

	if conn, err = ftp.dialer.Dial("tcp", addr); err != nil {
		return
	}

	if ftp.tlsconfig != nil {
		conn = tls.Client(conn, ftp.tlsconfig)
	}

	return
}

// System types from Syst
var (
	SystemTypeUnixL8    = "UNIX Type: L8"
	SystemTypeWindowsNT = "Windows_NT"
)

var reSystStatus = map[string]*regexp.Regexp{
	SystemTypeUnixL8:    regexp.MustCompile(""),
	SystemTypeWindowsNT: regexp.MustCompile(""),
}

/*func GetFilesList(path string) (files []string, err error) {

}*/

/*


// login on server with strange login behavior
func (ftp *FTP) SmartLogin(username string, password string) (err error) {
	var code int
	// Maybe the server has some useless words to say. Make him talk
	code, _ = ftp.RawCmd("NOOP")

	if code == 220 || code == 530 {
		// Maybe with another Noop the server will ask us to login?
		code, _ = ftp.RawCmd("NOOP")
		if code == 530 {
			// ok, let's login
			code, _ = ftp.RawCmd("USER %s", username)
			code, _ = ftp.RawCmd("NOOP")
			if code == 331 {
				// user accepted, password required
				code, _ = ftp.RawCmd("PASS %s", password)
				code, _ = ftp.RawCmd("PASS %s", password)
				if code == 230 {
					code, _ = ftp.RawCmd("NOOP")
					return
				}
			}
		}

	}
	// Nothing strange... let's try a normal login
	return ftp.Login(username, password)
}

*/
