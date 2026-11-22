package ril

import (
	"bytes"
	"encoding/binary"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

func mappingModem(t *testing.T, accepts []byte) *sit.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ipc")
	l, err := net.Listen("unixpacket", path)
	if err != nil {
		t.Skipf("no unixpacket sockets: %v", err)
	}
	t.Cleanup(func() { l.Close() })
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
			frame := make([]byte, 12)
			frame[0] = 1
			copy(frame[2:4], buf[2:4])
			binary.LittleEndian.PutUint16(frame[4:], uint16(len(frame)))
			copy(frame[6:10], buf[6:10])
			if binary.LittleEndian.Uint16(buf[2:]) != sit.SetSlotMapping || !bytes.Equal(buf[12:n], accepts) {
				frame[10] = 2
			}
			conn.Write(frame)
		}
	}()
	c, err := sit.Open("unix:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestASlotIsSelectedWithoutATable(t *testing.T) {
	for name, x := range map[string]struct {
		slot    int
		accepts []byte
	}{
		"eSIM on two lines":    {1, []byte{2, 1, 0, 0, 0, 0, 0, 0, 0}},
		"physical on two":      {0, []byte{2, 0, 0, 1, 0, 0, 0, 0, 0}},
		"eSIM on one line":     {1, []byte{1, 1, 0, 0, 0, 0, 0, 0, 0}},
		"eSIM on a legacy RIL": {1, []byte{2, 1, 0, 0, 0}},
	} {
		if err := SelectSlot(mappingModem(t, x.accepts), x.slot, time.Second); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSelectingASlotReportsARefusal(t *testing.T) {
	if err := SelectSlot(mappingModem(t, nil), 1, time.Second); err == nil {
		t.Fatal("a refused mapping was reported as done")
	}
	for _, slot := range []int{-1, 2} {
		if err := SelectSlot(nil, slot, time.Second); err == nil {
			t.Fatalf("slot %d was accepted", slot)
		}
	}
}
