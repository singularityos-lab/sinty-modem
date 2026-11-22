package recovery

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	AtomSocket = "/run/atom/control.sock"

	stackUnit   = "sinty-modem-stack.service"
	serviceUnit = "sinty-modemd.service"
	logLines    = 200
	maxReply    = 8 << 20
)

var identifiers = regexp.MustCompile(strings.Join([]string{
	`[0-9A-Fa-f]{0,4}(?::[0-9A-Fa-f]{0,4}){3,}`,
	`\b[0-9A-Fa-f]{1,4}::[0-9A-Fa-f]{0,4}`,
	`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`,
	`[0-9A-Fa-f]{16,}`,
	`\+?\(?[0-9](?:[ ().-]{0,2}[0-9]){5,}`,
}, "|"))

type request struct {
	Cmd  string `json:"cmd"`
	Unit string `json:"unit,omitempty"`
}

type reply struct {
	OK    bool     `json:"ok"`
	Error string   `json:"error,omitempty"`
	State string   `json:"state,omitempty"`
	Lines []string `json:"lines,omitempty"`
}

type Modem struct {
	Socket string
	Latch  string
}

func redact(line string) string { return identifiers.ReplaceAllString(line, "<hidden>") }

func (m Modem) ask(cmd, unit string) (reply, error) {
	var r reply
	c, err := net.DialTimeout("unix", m.Socket, 5*time.Second)
	if err != nil {
		return r, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(90 * time.Second))
	body, err := json.Marshal(request{cmd, unit})
	if err != nil {
		return r, err
	}
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	if _, err := c.Write(append(frame, body...)); err != nil {
		return r, err
	}
	var size [4]byte
	if _, err := io.ReadFull(c, size[:]); err != nil {
		return r, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n > maxReply {
		return r, errors.New("init reply too large")
	}
	out := make([]byte, n)
	if _, err := io.ReadFull(c, out); err != nil {
		return r, err
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return r, err
	}
	if !r.OK {
		return r, fmt.Errorf("%s %s: %s", cmd, unit, r.Error)
	}
	return r, nil
}

func (m Modem) restartUnit(unit string) error {
	_, err := m.ask("restart", unit)
	if !errors.Is(err, io.EOF) {
		return err
	}
	r, err := m.ask("status", unit)
	if err != nil {
		return err
	}
	if r.State != "active" && r.State != "activating" {
		return fmt.Errorf("%s is %s", unit, r.State)
	}
	return nil
}

func (m Modem) Restart() error {
	if _, err := m.ask("stop", serviceUnit); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if err := os.Remove(m.Latch); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := m.restartUnit(stackUnit); err != nil {
		return err
	}
	return m.restartUnit(serviceUnit)
}

func (m Modem) Diagnostics() string {
	var b strings.Builder
	if v, err := os.ReadFile(m.Latch); err == nil {
		fmt.Fprintf(&b, "bring-up attempts: %s\n", strings.TrimSpace(string(v)))
	}
	for _, unit := range []string{stackUnit, serviceUnit} {
		if r, err := m.ask("status", unit); err != nil {
			fmt.Fprintf(&b, "%s: %v\n", unit, err)
		} else {
			fmt.Fprintf(&b, "%s: %s\n", unit, r.State)
		}
		r, err := m.ask("logs", unit)
		if err != nil {
			fmt.Fprintf(&b, "  no log: %v\n", err)
			continue
		}
		for _, line := range r.Lines[max(0, len(r.Lines)-logLines):] {
			fmt.Fprintf(&b, "  %s\n", redact(line))
		}
	}
	return b.String()
}
