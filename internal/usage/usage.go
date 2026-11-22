package usage

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	netClass   = "/sys/class/net"
	bootIDFile = "/proc/sys/kernel/random/boot_id"
)

type counters struct {
	RX uint64 `json:"rx"`
	TX uint64 `json:"tx"`
}

type record struct {
	Month string              `json:"month"`
	Boot  string              `json:"boot"`
	Total counters            `json:"total"`
	Last  map[string]counters `json:"last"`
}

type Meter struct {
	file   string
	device func() string
	boot   string
	now    func() time.Time
	read   func(dev string) (rx, tx uint64, err error)

	mu  sync.Mutex
	rec record
}

func New(file string, device func() string) *Meter {
	m := &Meter{file: file, device: device, now: time.Now, read: readCounters}
	if b, err := os.ReadFile(bootIDFile); err == nil {
		m.boot = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(file); err == nil {
		if err := json.Unmarshal(b, &m.rec); err != nil {
			log.Printf("usage: counters unreadable: %v", err)
			m.rec = record{}
		}
	}
	return m
}

func readCounters(dev string) (uint64, uint64, error) {
	var v [2]uint64
	for i, name := range []string{"rx_bytes", "tx_bytes"} {
		b, err := os.ReadFile(filepath.Join(netClass, dev, "statistics", name))
		if err != nil {
			return 0, 0, err
		}
		if v[i], err = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); err != nil {
			return 0, 0, fmt.Errorf("%s %s: %w", dev, name, err)
		}
	}
	return v[0], v[1], nil
}

func grown(now, before uint64) uint64 {
	if now < before {
		return now
	}
	return now - before
}

func (m *Meter) Month() (rx, tx uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	if month := m.now().Format("2006-01"); month != m.rec.Month {
		m.rec.Month, m.rec.Total = month, counters{}
		changed = true
	}
	if m.boot != m.rec.Boot || m.rec.Last == nil {
		m.rec.Boot, m.rec.Last = m.boot, map[string]counters{}
		changed = true
	}
	if dev := m.device(); dev != "" {
		if r, t, err := m.read(dev); err == nil && (counters{r, t}) != m.rec.Last[dev] {
			last := m.rec.Last[dev]
			m.rec.Total.RX += grown(r, last.RX)
			m.rec.Total.TX += grown(t, last.TX)
			m.rec.Last[dev] = counters{r, t}
			changed = true
		}
	}
	if changed {
		if err := m.save(); err != nil {
			log.Printf("usage: counters not saved: %v", err)
		}
	}
	return m.rec.Total.RX, m.rec.Total.TX
}

func (m *Meter) save() error {
	if err := os.MkdirAll(filepath.Dir(m.file), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(m.rec)
	if err != nil {
		return err
	}
	if err := os.WriteFile(m.file+".new", b, 0o600); err != nil {
		return err
	}
	return os.Rename(m.file+".new", m.file)
}
