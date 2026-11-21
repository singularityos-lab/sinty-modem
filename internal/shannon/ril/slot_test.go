package ril

import (
	"context"
	"encoding/binary"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

func simStatusPayload(card byte, apps int) []byte {
	p := make([]byte, 3+apps*appRecord)
	p[0] = card
	p[2] = byte(apps)
	for i := 0; i < apps; i++ {
		r := p[3+i*appRecord:]
		r[0] = AppUSIM
		r[1] = 5
	}
	return p
}

func fakeChannel(t *testing.T, card byte, apps int) string {
	t.Helper()
	return settlingChannel(t, card, apps, 0)
}

// settlingChannel closes its first drops connections at once, the way the
// modem drops a channel opened while it is still settling.
func settlingChannel(t *testing.T, card byte, apps int, drops int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ipc")
	l, err := net.Listen("unixpacket", path)
	if err != nil {
		t.Skipf("no unixpacket sockets: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			if drops > 0 {
				drops--
				conn.Close()
				continue
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 4096)
				for {
					n, err := conn.Read(buf)
					if err != nil || n < 12 {
						return
					}
					id := binary.LittleEndian.Uint16(buf[2:])
					body := simStatusPayload(card, apps)
					if id != sit.SIMStatus {
						body = nil
					}
					frame := make([]byte, 12+len(body))
					frame[0] = 1
					binary.LittleEndian.PutUint16(frame[2:], id)
					binary.LittleEndian.PutUint16(frame[4:], uint16(len(frame)))
					copy(frame[6:10], buf[6:10])
					copy(frame[12:], body)
					conn.Write(frame)
				}
			}(conn)
		}
	}()
	return "unix:" + path
}

func pick(t *testing.T, slots []Slot) (*sit.Client, Slot) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, s, err := PickSlot(ctx, slots, sit.Open, time.Second, t.Logf)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, s
}

func TestPhysicalSIMWinsWhenPresent(t *testing.T) {
	_, s := pick(t, []Slot{
		{Name: "sim", Path: fakeChannel(t, CardPresent, 1)},
		{Name: "esim", Path: fakeChannel(t, CardPresent, 1)},
	})
	if s.Name != "sim" {
		t.Fatalf("picked %s, want the physical SIM", s.Name)
	}
}

func TestEmptyTrayFallsToTheESIM(t *testing.T) {
	c, s := pick(t, []Slot{
		{Name: "sim", Path: fakeChannel(t, CardAbsent, 0)},
		{Name: "esim", Path: fakeChannel(t, CardPresent, 1)},
	})
	if s.Name != "esim" {
		t.Fatalf("picked %s, want the eSIM", s.Name)
	}
	st, err := New(c, "").SIMStatus()
	if err != nil || !st.USIMReady() {
		t.Fatalf("the eSIM channel is not the one returned: %+v %v", st, err)
	}
}

func TestNoCardAnywhereKeepsThePhysicalSlot(t *testing.T) {
	_, s := pick(t, []Slot{
		{Name: "sim", Path: fakeChannel(t, CardAbsent, 0)},
		{Name: "esim", Path: fakeChannel(t, CardAbsent, 0)},
	})
	if s.Name != "sim" {
		t.Fatalf("picked %s, want the physical slot so the UI reports no SIM", s.Name)
	}
}

func TestMissingESIMChannelIsIgnored(t *testing.T) {
	_, s := pick(t, []Slot{
		{Name: "sim", Path: fakeChannel(t, CardAbsent, 0)},
		{Name: "esim", Path: "unix:" + filepath.Join(t.TempDir(), "absent")},
		{Name: "none", Path: ""},
	})
	if s.Name != "sim" {
		t.Fatalf("picked %s", s.Name)
	}
}

func TestAChannelClosedWhileSettlingIsOpenedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, s, err := PickSlot(ctx, []Slot{
		{Name: "sim", Path: fakeChannel(t, CardAbsent, 0)},
		{Name: "esim", Path: settlingChannel(t, CardPresent, 1, 2)},
	}, sit.Open, 5*time.Second, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if s.Name != "esim" {
		t.Fatalf("picked %s, want the eSIM once its channel settles", s.Name)
	}
	st, err := New(c, "").SIMStatus()
	if err != nil || !st.USIMReady() {
		t.Fatalf("the returned channel is not usable: %+v %v", st, err)
	}
}
