package props

import (
	"os"
	"strings"
	"testing"
)

func TestWriteForBionic(t *testing.T) {
	out := os.Getenv("SINTY_PROPS_OUT")
	if out == "" {
		t.Skip("set SINTY_PROPS_OUT to write an area for a bionic reader")
	}
	m, err := Create(out, DefaultSize)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	err = m.SetAll(map[string]string{
		"ro.boot.cdt_hwid":                      "0x00030306000100000100000000000000",
		"ro.boot.slot_suffix":                   "_a",
		"vendor.cbd.partition":                  "modem_a",
		"ro.vendor.cbd.modem_type":              "s5100sit",
		"persist.vendor.modem.efs.clear.action": "none",
		"ro.build.fingerprint":                  strings.Repeat("x", 120),
		"a":                                     "1",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestServeForBionic(t *testing.T) {
	out := os.Getenv("SINTY_PROPS_OUT")
	sock := os.Getenv("SINTY_PROPS_SOCKET")
	if out == "" || sock == "" {
		t.Skip("set SINTY_PROPS_OUT and SINTY_PROPS_SOCKET to serve a bionic client")
	}
	m, err := Create(out, DefaultSize)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.SetAll(map[string]string{"ro.property_service.version": "2", "vendor.x": "old"})
	got := make(chan string, 4)
	s, err := Serve(m.Area, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Observe = func(n, v string) { got <- n + "=" + v }
	ready := os.Getenv("SINTY_PROPS_READY")
	os.WriteFile(ready, []byte("1"), 0o644)
	for i := 0; i < 2; i++ {
		t.Log(<-got)
	}
	if v, _ := m.Get("vendor.x"); v != "new" {
		t.Fatalf("vendor.x = %q", v)
	}
	if v, _ := m.Get("vendor.added"); v != "yes" {
		t.Fatalf("vendor.added = %q", v)
	}
}
