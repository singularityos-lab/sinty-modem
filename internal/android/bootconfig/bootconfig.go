package bootconfig

import (
	"bufio"
	"io"
	"os"
	"strings"
)

func Read() (map[string]string, error) {
	f, err := os.Open("/proc/bootconfig")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f), nil
}

func Parse(r io.Reader) map[string]string {
	out := map[string]string{}
	s := bufio.NewScanner(r)
	for s.Scan() {
		k, v, ok := strings.Cut(s.Text(), "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

func AndroidProps(cfg map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range cfg {
		if rest, ok := strings.CutPrefix(k, "androidboot."); ok {
			out["ro.boot."+rest] = v
		}
	}
	return out
}
