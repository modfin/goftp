package goftp

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type dialFunc func(string, string) (net.Conn, error)

func (f dialFunc) Dial(network, address string) (net.Conn, error) { return f(network, address) }

type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *eventLog) add(e Event) { l.mu.Lock(); defer l.mu.Unlock(); l.events = append(l.events, e) }
func (l *eventLog) snapshot() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Event(nil), l.events...)
}
func (l *eventLog) phase(name string) []Event {
	var found []Event
	for _, e := range l.snapshot() {
		if e.Phase == name {
			found = append(found, e)
		}
	}
	return found
}

// All protocol tests use in-memory connections: no DNS or external FTP server.
func testPipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	server.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

func testFTP(t *testing.T, options Options, serve func(net.Conn, *bufio.Reader) error) *FTP {
	t.Helper()
	client, server := testPipe(t)
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		if _, err := io.WriteString(server, "220 Ready\r\n"); err != nil {
			done <- err
			return
		}
		done <- serve(server, bufio.NewReader(server))
	}()
	dataDialer := options.Dialer
	options.Dialer = dialFunc(func(network, address string) (net.Conn, error) {
		if network != "tcp" {
			return nil, fmt.Errorf("unexpected network %q", network)
		}
		if address == "ftp.test:21" {
			return client, nil
		}
		if dataDialer == nil {
			return nil, fmt.Errorf("unexpected data dial %q", address)
		}
		return dataDialer.Dial(network, address)
	})
	ftp, err := ConnectWithOptions("ftp.test:21", options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ftp.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("FTP server: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("FTP server did not exit")
		}
	})
	return ftp
}

func command(reader *bufio.Reader, conn net.Conn, want, reply string) error {
	got, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	if got != want+"\r\n" {
		return fmt.Errorf("command = %q, want %q", got, want)
	}
	if reply != "" {
		_, err = io.WriteString(conn, reply)
	}
	return err
}
func awaitClose(conn net.Conn) error { _, err := io.Copy(io.Discard, conn); return err }

func TestPasvReplies(t *testing.T) {
	cases := []struct {
		name, reply string
		want        error
		code        int
	}{
		{"success", "227 Entering Passive Mode (127,0,0,1,195,80)\r\n", nil, 227},
		{"multiline", "227-Preparing\r\n227 Entering Passive Mode (127,0,0,1,195,80)\r\n", nil, 227},
		{"missing_address", "227 Missing address\r\n", ErrPasvBadAnswer, 227},
		{"bad_number", "227 Passive (127,0,0,1,x,80)\r\n", ErrPasvBadAnswer, 227},
		{"bad_range", "227 Passive (127,0,0,1,256,80)\r\n", ErrPasvBadAnswer, 227},
		{"zero_port", "227 Passive (127,0,0,1,0,0)\r\n", ErrPasvBadAnswer, 227},
		{"rejected", "425 Cannot open passive listener\r\n", &replyError{}, 425},
		{"disconnected", "", io.EOF, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := &eventLog{}
			ftp := testFTP(t, Options{Observer: events.add}, func(c net.Conn, r *bufio.Reader) error {
				if err := command(r, c, "PASV", tc.reply); err != nil {
					return err
				}
				if tc.reply == "" {
					return nil
				}
				return awaitClose(c)
			})
			if ftp.pasvTimeout != 10*time.Second {
				t.Fatalf("default timeout = %v", ftp.pasvTimeout)
			}
			port, err := ftp.Pasv()
			if tc.name == "rejected" {
				var reply *replyError
				if !errors.As(err, &reply) {
					t.Fatalf("error = %v, want server reply", err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if err == nil && port != 50000 {
				t.Errorf("port = %d", port)
			}
			pasv := events.phase("pasv")
			if len(pasv) != 1 || pasv[0].PassivePort != port {
				t.Fatalf("passive port events: %+v", pasv)
			}
			replies := events.phase("command_reply")
			if len(replies) != 1 || replies[0].ReplyCode != tc.code {
				t.Fatalf("replies: %+v", replies)
			}
		})
	}
}

func TestPasvTimeout(t *testing.T) {
	for _, partial := range []string{"", "227 Passive (127,0,0,1,195,80)"} {
		t.Run(fmt.Sprintf("partial_%t", partial != ""), func(t *testing.T) {
			events := &eventLog{}
			ftp := testFTP(t, Options{PasvTimeout: 20 * time.Millisecond, Observer: events.add}, func(c net.Conn, r *bufio.Reader) error {
				if err := command(r, c, "PASV", partial); err != nil {
					return err
				}
				return awaitClose(c)
			})
			port, err := ftp.Pasv()
			if port != 0 || !errors.Is(err, ErrPasvTimeout) || err.Error() != "PasvTimeout" {
				t.Fatalf("Pasv = %d, %v", port, err)
			}
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("not a timeout: %v", err)
			}
			if err = ftp.Noop(); err == nil {
				t.Fatal("timed-out connection is still usable")
			}
			ftp.Close()
			closes := events.phase("close")
			if len(closes) != 1 || closes[0].CloseReason != "pasv_timeout" {
				t.Fatalf("close events: %+v", closes)
			}
			pasv := events.phase("pasv")
			if len(pasv) != 1 || pasv[0].ErrorKind != "pasv_timeout" {
				t.Fatalf("PASV events: %+v", pasv)
			}
		})
	}
}

func TestPasvTimeoutWhileSending(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ftp := testFTP(t, Options{PasvTimeout: 20 * time.Millisecond}, func(c net.Conn, r *bufio.Reader) error {
		<-release // Never read the command: Flush must be interrupted too.
		return nil
	})
	if _, err := ftp.Pasv(); !errors.Is(err, ErrPasvTimeout) {
		t.Fatalf("error = %v", err)
	}
}

type gatedReadConn struct {
	net.Conn
	failed  chan struct{}
	release chan struct{}
}

func (c *gatedReadConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		close(c.failed)
		<-c.release
	}
	return n, err
}

func TestPasvTimeoutWaitsForWorker(t *testing.T) {
	client, server := testPipe(t)
	gated := &gatedReadConn{Conn: client, failed: make(chan struct{}), release: make(chan struct{})}
	defer close(gated.release)
	ftp := &FTP{conn: gated, reader: bufio.NewReader(gated), writer: bufio.NewWriter(gated), pasvTimeout: 20 * time.Millisecond}
	go io.Copy(io.Discard, server)
	done := make(chan error, 1)
	go func() { _, err := ftp.Pasv(); done <- err }()
	select {
	case <-gated.failed:
	case <-time.After(time.Second):
		t.Fatal("timeout did not unblock read")
	}
	select {
	case err := <-done:
		t.Fatalf("Pasv returned before worker stopped: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	gated.release <- struct{}{}
	select {
	case err := <-done:
		if !errors.Is(err, ErrPasvTimeout) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Pasv did not return after worker stopped")
	}
}

func TestPasvReplyNearTimeout(t *testing.T) {
	for i := 0; i < 25; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			ftp := testFTP(t, Options{PasvTimeout: 2 * time.Millisecond}, func(c net.Conn, r *bufio.Reader) error {
				if err := command(r, c, "PASV", ""); err != nil {
					return nil
				} // Timeout may win even before the write.
				time.Sleep(2 * time.Millisecond)
				if _, err := io.WriteString(c, "227 Passive (127,0,0,1,195,80)\r\n"); err != nil {
					return nil
				}
				return awaitClose(c)
			})
			port, err := ftp.Pasv()
			if err == nil {
				if port != 50000 {
					t.Fatalf("port = %d", port)
				}
			} else if !errors.Is(err, ErrPasvTimeout) {
				t.Fatalf("unstable timeout result: %v", err)
			}
		})
	}
}

func TestConnectFailures(t *testing.T) {
	t.Run("dial", func(t *testing.T) {
		want := errors.New("sensitive dial details")
		events := &eventLog{}
		ftp, err := ConnectWithOptions("ftp.test:21", Options{Observer: events.add, Dialer: dialFunc(func(string, string) (net.Conn, error) { return nil, want })})
		if ftp != nil || !errors.Is(err, want) {
			t.Fatalf("Connect = %v, %v", ftp, err)
		}
		if got := events.phase("dial"); len(got) != 1 || got[0].ErrorKind != "error" {
			t.Fatalf("events: %+v", got)
		}
	})
	t.Run("greeting", func(t *testing.T) {
		client, server := testPipe(t)
		go func() { io.WriteString(server, "220 Partial greeting"); server.Close() }()
		events := &eventLog{}
		ftp, err := ConnectWithOptions("ftp.test:21", Options{Observer: events.add, Dialer: dialFunc(func(string, string) (net.Conn, error) { return client, nil })})
		if ftp != nil || !errors.Is(err, io.EOF) {
			t.Fatalf("Connect = %v, %v", ftp, err)
		}
		if got := events.phase("greeting"); len(got) != 1 || got[0].ErrorKind != "eof" {
			t.Fatalf("greeting: %+v", got)
		}
		if got := events.phase("close"); len(got) != 1 || got[0].CloseReason != "greeting_error" {
			t.Fatalf("close: %+v", got)
		}
	})
	t.Run("negative_timeout", func(t *testing.T) {
		ftp, err := ConnectWithOptions("ftp.test:21", Options{PasvTimeout: -time.Second, Dialer: dialFunc(func(string, string) (net.Conn, error) { t.Error("invalid options dialed"); return nil, io.EOF })})
		if ftp != nil || err == nil {
			t.Fatalf("Connect = %v, %v", ftp, err)
		}
	})
}

func TestObserverAndDebugRedactSecrets(t *testing.T) {
	var debug bytes.Buffer
	oldWriter, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&debug)
	log.SetFlags(0)
	defer func() { log.SetOutput(oldWriter); log.SetFlags(oldFlags) }()
	events := &eventLog{}
	ftp := testFTP(t, Options{Observer: events.add, Debug: true}, func(c net.Conn, r *bufio.Reader) error {
		if err := command(r, c, "USER private-user", "331 Send password for private-user\r\n"); err != nil {
			return err
		}
		if err := command(r, c, "PASS private-password", "530 Password private-password rejected\r\n"); err != nil {
			return err
		}
		if err := command(r, c, "private-command private-argument", "500 Echo private-command private-argument\r\n"); err != nil {
			return err
		}
		return awaitClose(c)
	})
	if err := ftp.Login("private-user", "private-password"); err == nil {
		t.Fatal("login unexpectedly succeeded")
	}
	ftp.RawCmd("%s %s", "private-command", "private-argument")
	ftp.Close()
	encoded, err := json.Marshal(events.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-user", "private-password", "private-command", "private-argument"} {
		if strings.Contains(string(encoded), secret) || strings.Contains(debug.String(), secret) {
			t.Errorf("logs leaked %q", secret)
		}
	}
	for _, event := range events.snapshot() {
		if event.Time.Location() != time.UTC || event.Duration < 0 {
			t.Errorf("invalid timing: %+v", event)
		}
	}
	replies := events.phase("command_reply")
	if len(replies) != 3 || replies[1].Command != "PASS" || replies[1].ReplyCode != 530 || replies[1].ErrorKind != "server_reply" || replies[2].Command != "UNKNOWN" {
		t.Fatalf("replies: %+v", replies)
	}
	if got := events.phase("login"); len(got) != 1 || got[0].ErrorKind != "server_reply" {
		t.Fatalf("login: %+v", got)
	}
}

func tlsConfigs(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ftp.test"}, DNSNames: []string{"ftp.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, &tls.Config{RootCAs: roots, ServerName: "ftp.test"}
}

func TestAuthTLS(t *testing.T) {
	serverConfig, clientConfig := tlsConfigs(t)
	events := &eventLog{}
	ftp := testFTP(t, Options{Observer: events.add}, func(c net.Conn, r *bufio.Reader) error {
		if err := command(r, c, "AUTH TLS", "234 Proceed\r\n"); err != nil {
			return err
		}
		secure := tls.Server(c, serverConfig)
		if err := secure.Handshake(); err != nil {
			return err
		}
		reader := bufio.NewReader(secure)
		if err := command(reader, secure, "PBSZ 0", "200 OK\r\n"); err != nil {
			return err
		}
		if err := command(reader, secure, "PROT P", "200 OK\r\n"); err != nil {
			return err
		}
		return awaitClose(secure)
	})
	if err := ftp.AuthTLS(clientConfig); err != nil {
		t.Fatal(err)
	}
	ftp.Close()
	for _, phase := range []string{"tls_handshake", "auth_tls"} {
		if got := events.phase(phase); len(got) != 1 || got[0].ErrorKind != "" {
			t.Fatalf("%s: %+v", phase, got)
		}
	}
}

func TestAuthTLSFailure(t *testing.T) {
	events := &eventLog{}
	ftp := testFTP(t, Options{Observer: events.add}, func(c net.Conn, r *bufio.Reader) error {
		if err := command(r, c, "AUTH TLS", "234 Proceed\r\n"); err != nil {
			return err
		}
		// Drain ClientHello before sending an invalid TLS response.
		header := make([]byte, 5)
		if _, err := io.ReadFull(c, header); err != nil {
			return err
		}
		body := make([]byte, int(header[3])<<8|int(header[4]))
		if _, err := io.ReadFull(c, body); err != nil {
			return err
		}
		_, err := io.WriteString(c, "not TLS")
		if err != nil {
			return err
		}
		return awaitClose(c)
	})
	if err := ftp.AuthTLS(&tls.Config{ServerName: "ftp.test"}); err == nil {
		t.Fatal("invalid TLS accepted")
	}
	if got := events.phase("tls_handshake"); len(got) != 1 || got[0].ErrorKind == "" {
		t.Fatalf("handshake: %+v", got)
	}
	if got := events.phase("close"); len(got) != 1 || got[0].CloseReason != "tls_error" {
		t.Fatalf("close: %+v", got)
	}
}
