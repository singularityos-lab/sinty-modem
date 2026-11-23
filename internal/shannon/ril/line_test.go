package ril

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

type sent struct {
	ID      uint16
	Payload []byte
}

type lineModem struct {
	mu      sync.Mutex
	got     []sent
	table   []byte
	refuses map[uint16]byte
}

func (m *lineModem) requests() []sent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sent(nil), m.got...)
}

func startLineModem(t *testing.T, m *lineModem) *sit.Client {
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
			id := binary.LittleEndian.Uint16(buf[2:])
			m.mu.Lock()
			m.got = append(m.got, sent{id, append([]byte(nil), buf[12:n]...)})
			code := m.refuses[id]
			m.mu.Unlock()
			var body []byte
			if id == sit.GetSlotStatus {
				body = m.table
			}
			frame := make([]byte, 12+len(body))
			frame[0] = 1
			copy(frame[2:4], buf[2:4])
			binary.LittleEndian.PutUint16(frame[4:], uint16(len(frame)))
			copy(frame[6:10], buf[6:10])
			frame[10] = code
			copy(frame[12:], body)
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

func pixelTable() []byte {
	return slotRecords(slotRecord, []byte{CardPresent, CardAbsent}, []byte{16, 0}, []byte{1, 0})
}

func TestTheStockPayloads(t *testing.T) {
	m := &lineModem{}
	c := startLineModem(t, m)
	if err := SetStandby(c, true, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := SetStandby(c, false, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := SetDataLine(c, 1, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := New(c, "").SetLinePower(true); err != nil {
		t.Fatal(err)
	}
	if err := New(c, "").SetLinePower(false); err != nil {
		t.Fatal(err)
	}
	want := []sent{
		{sit.SetStandby, []byte{1}},
		{sit.SetStandby, []byte{0}},
		{sit.SetDataLine, []byte{1}},
		{sit.SetRadioPower, []byte{2, 0, 0, 0, 0, 0}},
		{sit.SetRadioPower, []byte{1, 0, 0, 0, 0, 0}},
	}
	got := m.requests()
	if len(got) != len(want) {
		t.Fatalf("sent %d requests, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || !bytes.Equal(got[i].Payload, want[i].Payload) {
			t.Errorf("request %d = %s %v, want %s %v", i, sit.Name(got[i].ID), got[i].Payload, sit.Name(want[i].ID), want[i].Payload)
		}
	}
	if SetDataLine(c, 4, time.Second) == nil || SetDataLine(c, -1, time.Second) == nil {
		t.Fatal("a line outside the modem was accepted")
	}
}

func TestTheEUICCLineIsFound(t *testing.T) {
	st, err := ParseSlotStatus(pixelTable())
	if err != nil {
		t.Fatal(err)
	}
	if line, ok := EUICCLine(st); !ok || line != 1 {
		t.Fatalf("eUICC line = %d %t, want 1", line, ok)
	}
	empty, err := ParseSlotStatus(slotRecords(slotRecord, []byte{CardAbsent, CardAbsent}, []byte{16, 0}, []byte{1, 0}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := EUICCLine(empty); ok {
		t.Fatal("an empty eUICC was given a line")
	}
}

func TestAnESIMOnTheSecondLineGetsStandbyAndData(t *testing.T) {
	m := &lineModem{table: pixelTable()}
	c := startLineModem(t, m)
	file := filepath.Join(t.TempDir(), "standby")
	if err := ServeEUICCLine(c, file, t.Logf); err != nil {
		t.Fatal(err)
	}
	if err := ServeEUICCLine(c, file, t.Logf); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range m.requests() {
		ids = append(ids, sit.Name(r.ID))
	}
	want := []string{"GET_SLOT_STATUS", "SET_MODEMS_CONFIG", "SET_PREFERRED_DATA_MODEM", "GET_SLOT_STATUS", "SET_PREFERRED_DATA_MODEM"}
	if len(ids) != len(want) {
		t.Fatalf("sent %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("sent %v, want %v", ids, want)
		}
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("the standby choice was not kept")
	}
}

func TestARefusedStandbyIsNotRemembered(t *testing.T) {
	m := &lineModem{table: pixelTable(), refuses: map[uint16]byte{sit.SetStandby: 2}}
	c := startLineModem(t, m)
	file := filepath.Join(t.TempDir(), "standby")
	if err := ServeEUICCLine(c, file, t.Logf); err == nil {
		t.Fatal("a refused standby was reported as done")
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("a refused standby was remembered")
	}
	for _, r := range m.requests() {
		if r.ID == sit.SetDataLine {
			t.Fatal("data was moved after the standby was refused")
		}
	}
}

func TestAnESIMOnTheFirstLineIsLeftAlone(t *testing.T) {
	m := &lineModem{table: slotRecords(slotRecord, []byte{CardPresent, CardAbsent}, []byte{16, 0}, []byte{0, 1})}
	c := startLineModem(t, m)
	if err := ServeEUICCLine(c, filepath.Join(t.TempDir(), "standby"), t.Logf); err != nil {
		t.Fatal(err)
	}
	if got := m.requests(); len(got) != 1 || got[0].ID != sit.GetSlotStatus {
		t.Fatalf("sent %d requests for an eSIM already on the first line", len(got))
	}
}
