package netcfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/ril"
)

const (
	DNSDir     = "/run/sinty/dns.d"
	Metric     = "700"
	candidates = 8
)

type RMNet struct {
	State string

	mu   sync.Mutex
	devs map[string]string
}

func New(state string) *RMNet {
	r := &RMNet{State: state, devs: map[string]string{}}
	r.clearStale()
	return r
}

func ip(args ...string) error {
	cmd := exec.Command("ip", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func unused(dev string) bool {
	out, err := exec.Command("ip", "-j", "address", "show", "dev", dev).Output()
	if err != nil {
		return false
	}
	var info []struct {
		Flags    []string          `json:"flags"`
		AddrInfo []json.RawMessage `json:"addr_info"`
	}
	if json.Unmarshal(out, &info) != nil || len(info) == 0 {
		return false
	}
	for _, f := range info[0].Flags {
		if f == "UP" {
			return false
		}
	}
	return len(info[0].AddrInfo) == 0
}

func answers(dev string, targets []string) bool {
	for _, t := range targets {
		if exec.Command("ping", "-n", "-q", "-c", "1", "-W", "3", "-I", dev, t).Run() == nil {
			return true
		}
	}
	return false
}

func putBack(dev, address string) {
	ip("route", "del", "default", "dev", dev, "metric", Metric)
	ip("-6", "route", "del", "default", "dev", dev, "metric", Metric)
	if address != "" {
		ip("addr", "del", address+"/32", "dev", dev)
	}
	ip("addr", "flush", "dev", dev)
	ip("link", "set", "dev", dev, "down")
}

func (r *RMNet) record() string { return filepath.Join(r.State, "data-interface") }

func (r *RMNet) clearStale() {
	b, err := os.ReadFile(r.record())
	if err != nil {
		return
	}
	dev := strings.TrimSpace(string(b))
	if strings.HasPrefix(dev, "rmnet") {
		putBack(dev, "")
		os.Remove(filepath.Join(DNSDir, dev))
		log.Printf("net: removed a configuration left on %s by an earlier run", dev)
	}
	os.Remove(r.record())
}

func order(cid int) []string {
	first := cid - 1
	out := []string{fmt.Sprintf("rmnet%d", first)}
	for i := 0; i < candidates; i++ {
		if i != first {
			out = append(out, fmt.Sprintf("rmnet%d", i))
		}
	}
	return out
}

func (r *RMNet) Up(_ string, b ril.Bearer) error {
	v4, ok := b.IPv4()
	if !ok {
		return errors.New("the bearer has no IPv4 address")
	}
	dns := b.DNS()
	if len(dns) == 0 {
		return errors.New("the carrier sent no DNS server")
	}
	address := v4.String()
	targets := []string{}
	for _, d := range dns {
		if d.Is4() {
			targets = append(targets, d.String())
		}
	}
	targets = append(targets, "1.1.1.1", "8.8.8.8")
	os.MkdirAll(r.State, 0o755)
	for _, dev := range order(b.CID()) {
		if !unused(dev) {
			continue
		}
		os.WriteFile(r.record(), []byte(dev+"\n"), 0o644)
		kept := false
		err := ip("link", "set", "dev", dev, "up")
		if err == nil {
			err = ip("addr", "add", address+"/32", "dev", dev)
		}
		if err == nil {
			err = ip("route", "add", "default", "dev", dev, "src", address, "metric", Metric)
		}
		if err == nil {
			kept = answers(dev, targets)
		}
		if !kept {
			putBack(dev, address)
			os.Remove(r.record())
			continue
		}
		if v6, ok := b.IPv6(); ok && v6.IsGlobalUnicast() {
			if err := ip("-6", "addr", "add", v6.String()+"/64", "dev", dev, "nodad"); err == nil {
				ip("-6", "route", "add", "default", "dev", dev, "metric", Metric)
			}
		}
		if err := r.writeDNS(dev, b); err != nil {
			log.Printf("net: carrier DNS: %v", err)
		}
		r.mu.Lock()
		r.devs[fmt.Sprint(b.CID())] = dev
		r.mu.Unlock()
		log.Printf("net: bearer CID %d carried by %s", b.CID(), dev)
		return nil
	}
	return errors.New("no rmnet device carries the internet bearer")
}

func (r *RMNet) writeDNS(dev string, b ril.Bearer) error {
	if err := os.MkdirAll(DNSDir, 0o755); err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString("priority 200\n")
	for _, d := range b.DNS() {
		sb.WriteString("nameserver " + d.String() + "\n")
	}
	tmp := filepath.Join(DNSDir, "."+dev)
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(DNSDir, dev))
}

func (r *RMNet) Down(_ string) error {
	r.mu.Lock()
	devs := r.devs
	r.devs = map[string]string{}
	r.mu.Unlock()
	for _, dev := range devs {
		os.Remove(filepath.Join(DNSDir, dev))
		putBack(dev, "")
	}
	os.Remove(r.record())
	return nil
}
