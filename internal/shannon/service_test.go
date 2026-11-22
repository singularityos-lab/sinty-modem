package shannon

import (
	"context"
	"encoding/binary"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/phone"
	"github.com/singularityos-lab/sinty-modem/internal/shannon/ril"
	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
	"github.com/singularityos-lab/sinty-modem/internal/store"
)

func slotTable() []byte {
	p := make([]byte, 1+4*105)
	p[0] = 2
	first, second := p[1:], p[1+105:]
	first[0x34], second[0x34] = 1, 1
	second[0] = ril.CardPresent
	second[0x23] = 16
	second[0x40] = 0xff
	return p
}

type lateModem struct {
	conn   net.Conn
	listed atomic.Bool
	mapped atomic.Bool
}

func (m *lateModem) indicate(id uint16, body []byte) error {
	f := make([]byte, 8+len(body))
	f[0] = 2
	binary.LittleEndian.PutUint16(f[2:], id)
	binary.LittleEndian.PutUint16(f[4:], uint16(len(f)))
	copy(f[8:], body)
	_, err := m.conn.Write(f)
	return err
}

func (m *lateModem) serve() {
	buf := make([]byte, 4096)
	for {
		n, err := m.conn.Read(buf)
		if err != nil || n < 12 {
			return
		}
		id := binary.LittleEndian.Uint16(buf[2:])
		var body []byte
		switch id {
		case sit.GetSlotStatus:
			body = make([]byte, 1+4*105)
			if m.listed.Load() {
				body = slotTable()
			}
		case sit.SetSlotMapping:
			m.mapped.Store(true)
		case sit.SIMStatus:
			body = []byte{ril.CardAbsent, 0, 0}
			if m.mapped.Load() {
				body = make([]byte, 3+63)
				body[0], body[2], body[3], body[4] = ril.CardPresent, 1, ril.AppUSIM, 5
			}
		}
		f := make([]byte, 12+len(body))
		f[0] = 1
		binary.LittleEndian.PutUint16(f[2:], id)
		binary.LittleEndian.PutUint16(f[4:], uint16(len(f)))
		copy(f[6:10], buf[6:10])
		copy(f[12:], body)
		if _, err := m.conn.Write(f); err != nil {
			return
		}
	}
}

func startWithoutSIM(t *testing.T, retry time.Duration) (*lateModem, *Service) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ipc")
	l, err := net.Listen("unixpacket", path)
	if err != nil {
		t.Skipf("no unixpacket sockets: %v", err)
	}
	defer l.Close()
	c, err := sit.Open("unix:" + path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	m := &lateModem{conn: conn}
	go m.serve()
	st, err := store.Open(filepath.Join(dir, "messages"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(ril.New(c, ""), st, Settings{SIMTimeout: 10 * time.Millisecond, RetryDelay: retry}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		c.Close()
		conn.Close()
		<-done
	})
	until(t, "the missing SIM was never reported", func() bool { return s.Status().Stage == phone.StageFailed })
	return m, s
}

func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

func TestALateSlotIndicationBringsTheESIMUp(t *testing.T) {
	m, s := startWithoutSIM(t, time.Hour)
	if err := m.indicate(sit.IndSlotStatus, slotTable()); err != nil {
		t.Fatal(err)
	}
	until(t, "the indication did not map the eUICC", m.mapped.Load)
	until(t, "the eSIM was not picked up", func() bool { return s.Status().SIM.State == string(ril.SIMReady) })
}

func TestAFailedBringUpTriesTheSlotsAgain(t *testing.T) {
	m, s := startWithoutSIM(t, 20*time.Millisecond)
	m.listed.Store(true)
	until(t, "the slots were not read again", m.mapped.Load)
	until(t, "the eSIM was not picked up", func() bool { return s.Status().SIM.State == string(ril.SIMReady) })
}
