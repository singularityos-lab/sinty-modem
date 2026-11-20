package device

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const Dir = "/usr/share/sinty/devices"

var ErrNoModem = errors.New("this device declares no modem")

type Modem struct {
	Profile     string
	Driver      string
	ModemType   string
	LinkModules [][]string
	Modules     [][]string
	ASPM        string
	IPC         string
	ESIMIPC     string
	Router      string
	AudioCard   string
}

func Compatible() []string {
	b, err := os.ReadFile("/proc/device-tree/compatible")
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range bytes.Split(b, []byte{0}) {
		if len(c) > 0 {
			out = append(out, string(c))
		}
	}
	return out
}

const Applied = "/run/sinty/modem.conf"

func FindModem() (*Modem, error) {
	if _, err := os.Stat(Applied); err == nil {
		m, err := Load(Applied)
		if err != nil {
			return nil, err
		}
		if c := Compatible(); len(c) > 0 {
			m.Profile = c[0]
		}
		return m, nil
	}
	for _, c := range Compatible() {
		path := filepath.Join(Dir, c, "modem.conf")
		if _, err := os.Stat(path); err == nil {
			m, err := Load(path)
			if err != nil {
				return nil, err
			}
			m.Profile = c
			return m, nil
		}
	}
	return nil, ErrNoModem
}

func modules(v string) [][]string {
	var out [][]string
	for _, entry := range strings.Split(v, ";") {
		if f := strings.Fields(entry); len(f) > 0 {
			out = append(out, f)
		}
	}
	return out
}

func Load(path string) (*Modem, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := &Modem{IPC: "/dev/umts_ipc0", Router: "/dev/umts_router"}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "driver":
			m.Driver = v
		case "modem_type":
			m.ModemType = v
		case "link_modules":
			m.LinkModules = modules(v)
		case "modules":
			m.Modules = modules(strings.ReplaceAll(v, " ", ";"))
		case "aspm":
			m.ASPM = v
		case "ipc":
			m.IPC = v
		case "esim_ipc":
			m.ESIMIPC = v
		case "router":
			m.Router = v
		case "audio_card":
			m.AudioCard = v
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	if m.Driver == "" {
		return nil, errors.New(path + ": no driver")
	}
	return m, nil
}
