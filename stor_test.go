package goftp

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type dataResult struct {
	data []byte
	err  error
}

func TestStorStages(t *testing.T) {
	cases := []struct {
		name, preliminary, completion       string
		dialFailure, sourceFailure, tlsData bool
	}{
		{name: "150_success", preliminary: "150", completion: "226"},
		{name: "125_success", preliminary: "125", completion: "226"},
		{name: "rejected_before_copy", preliminary: "550"},
		{name: "rejected_after_copy", preliminary: "150", completion: "450"},
		{name: "closed_before_completion", preliminary: "150"},
		{name: "data_dial_failure", dialFailure: true},
		{name: "partial_copy_failure", preliminary: "150", sourceFailure: true},
		{name: "tls_data", preliminary: "150", completion: "226", tlsData: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataClient, dataServer := testPipe(t)
			dataDone := make(chan dataResult, 1)
			var clientTLS *tls.Config
			if tc.tlsData {
				serverTLS, config := tlsConfigs(t)
				clientTLS = config
				dataServer = tls.Server(dataServer, serverTLS)
			}
			if !tc.dialFailure {
				go func() { data, err := io.ReadAll(dataServer); dataServer.Close(); dataDone <- dataResult{data, err} }()
			}
			events := &eventLog{}
			dialError := errors.New("data dial failed")
			sourceError := errors.New("source failed")
			options := Options{Observer: events.add, Dialer: dialFunc(func(network, address string) (net.Conn, error) {
				if network != "tcp" || address != "ftp.test:50000" {
					return nil, fmt.Errorf("wrong data dial %s %s", network, address)
				}
				if tc.dialFailure {
					return nil, dialError
				}
				return dataClient, nil
			})}
			var received []byte
			ftp := testFTP(t, options, func(c net.Conn, r *bufio.Reader) error {
				if err := command(r, c, "TYPE I", "200 Binary\r\n"); err != nil {
					return err
				}
				if err := command(r, c, "PASV", "227 Passive (127,0,0,1,195,80)\r\n"); err != nil {
					return err
				}
				if err := command(r, c, "STOR /private-file.json", ""); err != nil {
					return err
				}
				if tc.dialFailure {
					return awaitClose(c)
				}
				if _, err := io.WriteString(c, tc.preliminary+" Transfer\r\n"); err != nil {
					return err
				}
				result := <-dataDone
				if result.err != nil {
					return result.err
				}
				received = result.data
				if tc.preliminary == "550" || tc.sourceFailure {
					return awaitClose(c)
				}
				if tc.completion == "" {
					return nil
				}
				if _, err := io.WriteString(c, tc.completion+" Result\r\n"); err != nil {
					return err
				}
				return awaitClose(c)
			})
			// Exercise the existing PROT P data path, independently of AUTH TLS tests.
			ftp.tlsconfig = clientTLS
			payload := "private-payload"
			var source io.Reader = strings.NewReader(payload)
			if tc.sourceFailure {
				source = io.MultiReader(strings.NewReader("abc"), errorReader{sourceError})
			}
			err := ftp.Stor("/private-file.json", source)
			wantSuccess := tc.completion == "226"
			if (err == nil) != wantSuccess {
				t.Fatalf("Stor error = %v", err)
			}
			if tc.dialFailure && !errors.Is(err, dialError) {
				t.Fatalf("lost dial cause: %v", err)
			}
			if tc.sourceFailure && !errors.Is(err, sourceError) {
				t.Fatalf("lost source cause: %v", err)
			}
			if tc.name == "closed_before_completion" && !errors.Is(err, io.EOF) {
				t.Fatalf("lost close cause: %v", err)
			}
			ftp.Close()
			store := events.phase("store")
			if len(store) != 1 {
				t.Fatalf("store events: %+v", store)
			}
			var wantBytes int64
			if !tc.dialFailure && tc.preliminary != "550" {
				wantBytes = int64(len(payload))
			}
			if tc.sourceFailure {
				wantBytes = 3
			}
			if store[0].Bytes != wantBytes || ((store[0].ErrorKind == "") != wantSuccess) {
				t.Fatalf("store: %+v", store[0])
			}
			if got := events.phase("data_dial"); len(got) != 1 || ((got[0].ErrorKind != "") != tc.dialFailure) {
				t.Fatalf("data dial: %+v", got)
			}
			if tc.dialFailure {
				if got := events.phase("stor_preliminary"); len(got) != 0 {
					t.Fatalf("unexpected preliminary: %+v", got)
				}
			} else {
				preliminary := events.phase("stor_preliminary")
				if len(preliminary) != 1 || preliminary[0].ReplyCode != replyCode(tc.preliminary) {
					t.Fatalf("preliminary: %+v", preliminary)
				}
			}
			if wantBytes > 0 {
				copies := events.phase("data_copy")
				if len(copies) != 1 || copies[0].Bytes != wantBytes || ((copies[0].ErrorKind != "") != tc.sourceFailure) {
					t.Fatalf("copy: %+v", copies)
				}
			}
			if !tc.dialFailure && tc.preliminary != "550" && !tc.sourceFailure {
				completion := events.phase("stor_completion")
				if len(completion) != 1 || completion[0].ReplyCode != replyCode(tc.completion) {
					t.Fatalf("completion: %+v", completion)
				}
			}
			encoded, _ := json.Marshal(events.snapshot())
			if bytes.Contains(encoded, []byte("private-file")) || bytes.Contains(encoded, []byte(payload)) {
				t.Fatal("observer leaked path or payload")
			}
			// The server writes the completion reply only after reading the whole stream.
			if wantSuccess && string(received) != payload {
				t.Fatalf("received %q", received)
			}
		})
	}
}

func TestQuitClosesOnce(t *testing.T) {
	events := &eventLog{}
	ftp := testFTP(t, Options{Observer: events.add}, func(c net.Conn, r *bufio.Reader) error {
		if err := command(r, c, "QUIT", "221 Goodbye\r\n"); err != nil {
			return err
		}
		return awaitClose(c)
	})
	if err := ftp.Quit(); err != nil {
		t.Fatal(err)
	}
	if err := ftp.Close(); err != nil {
		t.Fatal(err)
	}
	if got := events.phase("close"); len(got) != 1 || got[0].CloseReason != "quit" {
		t.Fatalf("close: %+v", got)
	}
}

func TestSuccessfulContentThenHeadersTimeout(t *testing.T) {
	dataClient, dataServer := testPipe(t)
	dataDone := make(chan dataResult, 1)
	go func() {
		data, err := io.ReadAll(dataServer)
		dataServer.Close()
		dataDone <- dataResult{data, err}
	}()
	events := &eventLog{}
	ftp := testFTP(t, Options{
		Observer: events.add, PasvTimeout: 20 * time.Millisecond,
		Dialer: dialFunc(func(string, string) (net.Conn, error) { return dataClient, nil }),
	}, func(c net.Conn, r *bufio.Reader) error {
		if err := command(r, c, "TYPE I", "200 Binary\r\n"); err != nil {
			return err
		}
		if err := command(r, c, "PASV", "227 Passive (127,0,0,1,195,80)\r\n"); err != nil {
			return err
		}
		if err := command(r, c, "STOR /item.json", "150 Transfer\r\n"); err != nil {
			return err
		}
		result := <-dataDone
		if result.err != nil || string(result.data) != "content" {
			return fmt.Errorf("content upload: %q, %v", result.data, result.err)
		}
		if _, err := io.WriteString(c, "226 Stored\r\n"); err != nil {
			return err
		}
		if err := command(r, c, "TYPE I", "200 Binary\r\n"); err != nil {
			return err
		}
		if err := command(r, c, "PASV", ""); err != nil {
			return err
		}
		// The companion upload must stop here, before sending STOR.
		unexpected, err := io.ReadAll(c)
		if len(unexpected) != 0 {
			return fmt.Errorf("commands after PASV timeout: %q", unexpected)
		}
		return err
	})
	if err := ftp.Stor("/item.json", strings.NewReader("content")); err != nil {
		t.Fatal(err)
	}
	if err := ftp.Stor("/item.json.headers", strings.NewReader("headers")); !errors.Is(err, ErrPasvTimeout) {
		t.Fatalf("headers error = %v", err)
	}
	stores := events.phase("store")
	if len(stores) != 2 || stores[0].ErrorKind != "" || stores[0].Bytes != 7 ||
		stores[1].ErrorKind != "pasv_timeout" || stores[1].Bytes != 0 {
		t.Fatalf("store events: %+v", stores)
	}
	completions := events.phase("stor_completion")
	if len(completions) != 1 || completions[0].ReplyCode != 226 {
		t.Fatalf("completion events: %+v", completions)
	}
}
