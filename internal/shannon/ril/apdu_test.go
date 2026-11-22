package ril

import (
	"bytes"
	"encoding/binary"
	"net"
	"path/filepath"
	"testing"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

func profileCard(t *testing.T, closed chan<- uint32) *sit.Client {
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
			p := buf[12:n]
			var body []byte
			var refused byte
			switch binary.LittleEndian.Uint16(buf[2:]) {
			case sit.OpenChannel:
				if len(p) != 18 || p[0] != 16 || !bytes.Equal(p[1:17], isdr) || p[17] != 0 {
					refused = 2
					break
				}
				body = []byte{2, 0, 0, 0, 0x90, 0, 0, 0}
			case sit.TransmitAPDU:
				want := []uint32{2, 0x82, 0xe2, 0x91, 0, 3}
				for i, w := range want {
					if len(p) != 26+2*3 || binary.LittleEndian.Uint32(p[i*4:]) != w {
						refused = 2
					}
				}
				if refused == 0 && (binary.LittleEndian.Uint16(p[24:]) != 3 || !bytes.Equal(p[26:29], []byte{0xbf, 0x2d, 0})) {
					refused = 2
				}
				body = []byte{0x61, 0x10, 2, 0, 0xbf, 0x2d}
			case sit.CloseChannel:
				closed <- binary.LittleEndian.Uint32(p)
			}
			frame := make([]byte, 12+len(body))
			frame[0], frame[10] = 1, refused
			copy(frame[2:4], buf[2:4])
			binary.LittleEndian.PutUint16(frame[4:], uint16(len(frame)))
			copy(frame[6:10], buf[6:10])
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

func TestAProfileCommandCrossesTheLogicalChannel(t *testing.T) {
	closed := make(chan uint32, 1)
	e := New(profileCard(t, closed), "").EUICC()
	channel, err := e.OpenLogicalChannel(isdr)
	if err != nil || channel != 2 {
		t.Fatalf("open %d %v", channel, err)
	}
	got, err := e.Transmit([]byte{0x82, 0xe2, 0x91, 0, 3, 0xbf, 0x2d, 0})
	if err != nil || !bytes.Equal(got, []byte{0xbf, 0x2d, 0x61, 0x10}) {
		t.Fatalf("transmit % x %v", got, err)
	}
	if err := e.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if c := <-closed; c != 2 {
		t.Fatalf("closed channel %d", c)
	}
	if _, err := e.Transmit([]byte{0x82, 0xe2, 0x91, 0, 0}); err == nil {
		t.Fatal("a command went out on a closed channel")
	}
}

func TestOnlyTheProfileManagerCanBeOpened(t *testing.T) {
	e := New(nil, "").EUICC()
	if _, err := e.OpenLogicalChannel([]byte{0xa0, 0, 0, 0, 0x87, 0x10, 2}); err == nil {
		t.Fatal("another application was opened")
	}
}

func TestMalformedCardCommandsAreRefused(t *testing.T) {
	for _, cmd := range [][]byte{{0x80, 0xe2}, {0x80, 0xe2, 0x91, 0, 9, 1}} {
		if _, err := apduRequest(1, cmd); err == nil {
			t.Errorf("% x was accepted", cmd)
		}
	}
	p, err := apduRequest(1, []byte{0x81, 0xc0, 0, 0, 0x20})
	if err != nil || len(p) != 26 || binary.LittleEndian.Uint32(p[20:]) != 0x20 || p[24] != 0 {
		t.Fatalf("get response % x %v", p, err)
	}
}
