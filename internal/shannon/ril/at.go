package ril

import (
	"errors"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/cpif"
)

var (
	atFinal = regexp.MustCompile(`(?:^|[\r\n])(OK|ERROR|\+CME ERROR[^\r\n]*)[\r\n]`)
	atOK    = regexp.MustCompile(`(?:^|[\r\n])OK(?:[\r\n]|$)`)
	atCFUN  = regexp.MustCompile(`\+CFUN:\s*(\d+)`)
)

func atExchange(path, command string, timeout time.Duration) (string, error) {
	f, err := cpif.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write([]byte(command + "\r")); err != nil {
		return "", err
	}
	deadline := time.Now().Add(timeout)
	f.SetReadDeadline(deadline)
	var resp []byte
	buf := make([]byte, 4096)
	for time.Now().Before(deadline) {
		n, err := f.Read(buf)
		if n > 0 {
			resp = append(resp, buf[:n]...)
			if len(resp) > 16384 {
				return "", errors.New("AT response exceeds the limit")
			}
			if atFinal.Match(resp) {
				break
			}
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				break
			}
			return string(resp), err
		}
	}
	return string(resp), nil
}

func (r *RIL) SetRadio(on bool) error {
	cmd := "AT+CFUN=0"
	if on {
		cmd = "AT+CFUN=1"
	}
	resp, err := atExchange(r.router, cmd, 25*time.Second)
	if err != nil {
		return err
	}
	if !atOK.MatchString(resp) {
		return errors.New("the radio command was not accepted")
	}
	return nil
}

func (r *RIL) RadioLevel() (int, error) {
	resp, err := atExchange(r.router, "AT+CFUN?", 10*time.Second)
	if err != nil {
		return -1, err
	}
	m := atCFUN.FindStringSubmatch(resp)
	if m == nil || !atOK.MatchString(resp) {
		return -1, errors.New("unreadable radio state")
	}
	return strconv.Atoi(m[1])
}
