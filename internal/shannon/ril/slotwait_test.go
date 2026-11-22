package ril

import (
	"encoding/binary"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

func enumeratingModem(t *testing.T, pending int, settle time.Duration) (string, *atomic.Bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ipc")
	l, err := net.Listen("unixpacket", path)
	if err != nil {
		t.Skipf("no unixpacket sockets: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	mapped := &atomic.Bool{}
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if err != nil || n < 12 {
				return
			}
			id := binary.LittleEndian.Uint16(buf[2:])
			var body []byte
			switch id {
			case sit.GetSlotStatus:
				body = slotRecords(slotRecord, []byte{CardAbsent, CardPresent}, []byte{0, 16}, []byte{0, unmapped})
				if pending > 0 {
					pending--
					body = make([]byte, len(body))
				}
			case sit.SetSlotMapping:
				time.Sleep(settle)
				mapped.Store(true)
			case sit.SIMStatus:
				body = simStatusPayload(CardAbsent, 0)
				if mapped.Load() {
					body = simStatusPayload(CardPresent, 1)
				}
			}
			frame := make([]byte, 12+len(body))
			frame[0] = 1
			binary.LittleEndian.PutUint16(frame[2:], id)
			binary.LittleEndian.PutUint16(frame[4:], uint16(len(frame)))
			copy(frame[6:10], buf[6:10])
			copy(frame[12:], body)
			conn.Write(frame)
		}
	}()
	return "unix:" + path, mapped
}

func TestMappingWaitsForTheModemToListItsSlots(t *testing.T) {
	path, mapped := enumeratingModem(t, 2, 0)
	c, err := sit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := MapEUICC(c, 10*time.Second, t.Logf); err != nil {
		t.Fatal(err)
	}
	if !mapped.Load() {
		t.Fatal("the eUICC was never mapped")
	}
}

func TestMappingWaitsForASlowModem(t *testing.T) {
	path, mapped := enumeratingModem(t, 0, 9*time.Second)
	c, err := sit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := MapEUICC(c, 10*time.Second, t.Logf); err != nil {
		t.Fatal(err)
	}
	if !mapped.Load() {
		t.Fatal("the eUICC was never mapped")
	}
}
