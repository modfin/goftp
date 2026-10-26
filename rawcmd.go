package goftp

import "strconv"

// RawCmd sends raw commands to the remote server. Returns response code as int and response as string.
func (ftp *FTP) RawCmd(command string, args ...interface{}) (code int, line string) {
	code = -1
	var err error
	if err = ftp.send(command, args...); err != nil {
		return code, ""
	}
	if line, err = ftp.receive(); err != nil {
		return code, ""
	}
	code, err = strconv.Atoi(StatusCode(line))
	return code, line
}
