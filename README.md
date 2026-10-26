goftp
=====

Golang FTP library with Walk support.

## Features

* AUTH TLS support
* Walk 

## Sample
```go
package main

import (
    "crypto/sha256"
    "crypto/tls"
    "fmt"
    "io"
    "os"

    "encoding/hex"
    "github.com/modfin/goftp"
)

func main() {
    var err error
    var ftp *goftp.FTP

    // For debug messages: goftp.ConnectDbg("ftp.server.com:21")
    if ftp, err = goftp.Connect("ftp.server.com:21"); err != nil {
        panic(err)
    }

    defer ftp.Close()
    fmt.Println("Successfully connected to", server)

    // TLS client authentication
    config := tls.Config{
        InsecureSkipVerify: true,
        ClientAuth:         tls.RequestClientCert,
    }

    if err = ftp.AuthTLS(config); err != nil {
        panic(err)
    }

    // Username / password authentication
    if err = ftp.Login("username", "password"); err != nil {
        panic(err)
    }

    if err = ftp.Cwd("/"); err != nil {
        panic(err)
    }

    var curpath string
    if curpath, err = ftp.Pwd(); err != nil {
        panic(err)
    }

    fmt.Printf("Current path: %s", curpath)

    // Get directory listing
    var files []string
    if files, err = ftp.List(""); err != nil {
        panic(err)
    }
    fmt.Println("Directory listing:", files)

    // Upload a file
    var file *os.File
    if file, err = os.Open("/tmp/test.txt"); err != nil {
        panic(err)
    }

    if err := ftp.Stor("/test.txt", file); err != nil {
        panic(err)
    }

    // Download each file into local memory, and calculate it's sha256 hash
    err = ftp.Walk("/", func(path string, info os.FileMode, err error) error {
        _, err = ftp.Retr(path, func(r io.Reader) error {
            var hasher = sha256.New()
            if _, err = io.Copy(hasher, r); err != nil {
                return err
            }

            hash := fmt.Sprintf("%s %x", path, hex.EncodeToString(hasher.Sum(nil)))
            fmt.Println(hash)

            return err
        })

        return nil
    })
}
````

## Contributions

Contributions are welcome.

* Sourav Datta: for his work on the anonymous user login and multiline return status.
* Vincenzo La Spesa: for his work on resolving login issues with specific ftp servers


## Creators

**Remco Verhoef**
- <https://twitter.com/remco_verhoef>
- <https://twitter.com/dutchcoders>

## Copyright and license

Code and documentation copyright 2011-2014 Remco Verhoef.
Code released under [the MIT license](LICENSE).


## PASV timeouts and structured diagnostics

Existing `Connect`, `ConnectDbg` and custom-dialer entry points remain available.
Use `ConnectWithOptions` to observe the connection from the initial dial:

```go
conn, err := goftp.ConnectWithOptions("ftp.example.com:21", goftp.Options{
    Dialer: customDialer, // optional; also used for passive data connections
    // PasvTimeout: 10 * time.Second, // zero keeps the 10-second default
    Observer: func(event goftp.Event) {
        // Add application attempt/session IDs here. Event.Time is UTC.
        // Duration is a time.Duration (nanoseconds in JSON).
        log.Printf("time=%s phase=%s command=%s reply=%d passive_port=%d duration=%s bytes=%d error_kind=%s close_reason=%s",
            event.Time.Format(time.RFC3339Nano), event.Phase, event.Command,
            event.ReplyCode, event.PassivePort, event.Duration, event.Bytes,
            event.ErrorKind, event.CloseReason)
    },
})
if err != nil {
    return err
}
defer conn.Close()
```

`PasvTimeout` covers sending `PASV` and receiving/parsing its complete `227`
reply. It does not cover login, `TYPE I`, data dialing, uploading or waiting for
`226`. On expiry, the library closes the control connection, waits for the
pending command to stop, and returns `ErrPasvTimeout` (text: `PasvTimeout`). Use
`errors.Is(err, goftp.ErrPasvTimeout)` to recognize this condition; it also
implements `net.Error` with `Timeout() == true`. Retry on a new connection.
This fixes the competing writes that could replace the timeout with a closed
connection error; it does not resolve server or network latency.

| Phase | Meaning |
| --- | --- |
| `dial`, `greeting` | TCP/custom-dialer setup and initial server reply |
| `auth_tls`, `tls_handshake`, `login` | Setup operations and TLS handshake |
| `command_send` | Writing and flushing a command, without its arguments |
| `command_reply` | First reply latency, measured from the start of command sending |
| `pasv` | Entire PASV exchange, including its authoritative timeout result |
| `data_dial` | Opening the passive data connection using the configured dialer |
| `stor_preliminary` | STOR reply (`125`/`150`), measured from command sending |
| `data_copy` | Upload stream copy, including any data-channel TLS handshake |
| `stor_completion` | Waiting for the final reply (`226`), after closing the data connection |
| `store` | Entire file upload; bytes copied are included even on later failure |
| `close` | Control connection closure and its cause |

Durations of enclosing operations include their child operations and should not
be added together. `Bytes` means bytes copied to the data connection, not proof
that the server stored the file: only a successful `store` event confirms the
final reply. `CloseReason` is `caller`, `quit`, `pasv_timeout`, `greeting_error`
or `tls_error`. A local timeout can produce a `closed` command-reply event before
the enclosing `pasv` event reports `pasv_timeout`.

Events include numeric reply codes, passive ports and error categories, never raw server
replies, command arguments, filenames, passwords or payloads. The legacy debug
entry points now log these same redacted fields. Original errors are still
returned to callers; applications should avoid logging them blindly because
server replies can contain sensitive text.

Observers run synchronously and are serialized per connection, including events
from the PASV worker. They must return promptly and must not call back into the
connection. Synchronize an observer shared across connections. FTP operations
on a connection must be sequential. Configure non-PASV deadlines through your
custom dialer/connection as before; this change adds no overall transfer deadline.

## Tests

The offline tests use `net.Pipe` and include protocol failures, TLS, log redaction
and timeout races. This repository retains its existing layout without a
`go.mod`; run with Go 1.16 or later:

```sh
GO111MODULE=off go test -race ./...
```

The older tests that contact public FTP servers are opt-in:

```sh
GO111MODULE=off go test -tags=integration ./...
```
