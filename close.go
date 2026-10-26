package goftp

import "time"

// Close ends the FTP connection. Repeated calls return the first close result.
func (ftp *FTP) Close() error {
	return ftp.closeWithReason("caller")
}

func (ftp *FTP) closeWithReason(reason string) error {
	ftp.closeOnce.Do(func() {
		start := time.Now()
		ftp.closeErr = ftp.conn.Close()
		ftp.observe(Event{Phase: "close", CloseReason: reason}, start, ftp.closeErr)
	})
	return ftp.closeErr
}
