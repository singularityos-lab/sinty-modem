package usage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestUsageSurvivesRebootsAndStartsAgainEachMonth(t *testing.T) {
	file := filepath.Join(t.TempDir(), "usage.json")
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	boot, dev := "a", "rmnet0"
	var rx, tx uint64
	open := func() *Meter {
		m := New(file, func() string { return dev })
		m.now = func() time.Time { return now }
		m.boot = boot
		m.read = func(string) (uint64, uint64, error) { return rx, tx, nil }
		return m
	}
	check := func(m *Meter, wantRX, wantTX uint64) {
		t.Helper()
		if gotRX, gotTX := m.Month(); gotRX != wantRX || gotTX != wantTX {
			t.Fatalf("got %d %d, want %d %d", gotRX, gotTX, wantRX, wantTX)
		}
	}
	m := open()
	rx, tx = 100, 10
	check(m, 100, 10)
	rx, tx = 150, 15
	check(m, 150, 15)
	check(open(), 150, 15)
	boot, rx, tx = "b", 400, 40
	m = open()
	check(m, 550, 55)
	rx, tx = 20, 2
	check(m, 570, 57)
	now = now.AddDate(0, 0, 3)
	rx, tx = 50, 5
	check(m, 30, 3)
	dev = ""
	check(m, 30, 3)
}
