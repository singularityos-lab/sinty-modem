package vendor

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func moduleDir(name string) string {
	return filepath.Join("/sys/module", strings.ReplaceAll(name, "-", "_"))
}

func paramMatches(have, want string) bool {
	return have == want || (want == "1" && have == "Y") || (want == "0" && have == "N")
}

func checkLoaded(entry []string) error {
	dir := moduleDir(entry[0])
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	for _, kv := range entry[1:] {
		k, v, _ := strings.Cut(kv, "=")
		have, err := os.ReadFile(filepath.Join(dir, "parameters", k))
		if err != nil || !paramMatches(strings.TrimSpace(string(have)), v) {
			return fmt.Errorf("%s is loaded without %s: the device profile's modprobe.conf did not apply before coldplug, a reboot is needed", entry[0], kv)
		}
	}
	return nil
}

func LoadModules(link, rest [][]string, aspm string) error {
	for _, e := range link {
		if err := checkLoaded(e); err != nil {
			return err
		}
	}
	for _, e := range append(append([][]string{}, link...), rest...) {
		if out, err := exec.Command("modprobe", e...).CombinedOutput(); err != nil {
			return fmt.Errorf("modprobe %s: %v: %s", strings.Join(e, " "), err, strings.TrimSpace(string(out)))
		}
	}
	if aspm != "" {
		if err := os.WriteFile("/sys/module/pcie_aspm/parameters/policy", []byte(aspm), 0); err != nil {
			log.Printf("vendor: ASPM policy %s: %v", aspm, err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat("/dev/umts_boot0"); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("the modem ports did not appear after loading the drivers")
}

type Latch struct {
	Path  string
	Build string
}

func (l Latch) Attempts() int {
	b, err := os.ReadFile(l.Path)
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	build := ""
	if len(fields) >= 3 {
		build = fields[2]
	}
	if build != l.Build {
		return 0
	}
	n, _ := strconv.Atoi(fields[0])
	return n
}

func (l Latch) Arm() (int, error) {
	n := l.Attempts() + 1
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return n, err
	}
	f, err := os.OpenFile(l.Path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return n, err
	}
	line := fmt.Sprintf("%d %s", n, time.Now().Format(time.RFC3339))
	if l.Build != "" {
		line += " " + l.Build
	}
	fmt.Fprintln(f, line)
	err = f.Sync()
	f.Close()
	return n, err
}

func (l Latch) Clear() error {
	err := os.Remove(l.Path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func OSBuild(paths ...string) string {
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "BUILD_ID="); ok {
				return strings.Trim(strings.TrimSpace(v), "\"'")
			}
		}
	}
	return ""
}
