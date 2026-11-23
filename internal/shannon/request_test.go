package shannon

import (
	"bytes"
	"encoding/binary"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/ril"
	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
	"github.com/singularityos-lab/sinty-modem/internal/store"
)

type rawLine struct {
	mu   sync.Mutex
	seen []string
}

func (l *rawLine) got() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.seen...)
}

func serveRawLine(t *testing.T, name string, code byte, reply []byte) (string, *rawLine) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	l, err := net.Listen("unixpacket", path)
	if err != nil {
		t.Skipf("no unixpacket sockets: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	line := &rawLine{}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 4096)
				for {
					n, err := conn.Read(buf)
					if err != nil || n < 12 {
						return
					}
					line.mu.Lock()
					line.seen = append(line.seen, sit.Name(binary.LittleEndian.Uint16(buf[2:]))+" "+string(rune('0'+len(buf[12:n]))))
					line.mu.Unlock()
					frame := make([]byte, 12+len(reply))
					frame[0] = 1
					copy(frame[2:4], buf[2:4])
					binary.LittleEndian.PutUint16(frame[4:], uint16(len(frame)))
					copy(frame[6:10], buf[6:10])
					frame[10] = code
					copy(frame[12:], reply)
					conn.Write(frame)
				}
			}()
		}
	}()
	return "unix:" + path, line
}

func TestDeveloperRequestsReachEitherLine(t *testing.T) {
	first, firstSeen := serveRawLine(t, "ipc0", 0, []byte{7, 8})
	second, secondSeen := serveRawLine(t, "ipc1", 6, []byte{9})
	c, err := sit.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	st, err := store.Open(filepath.Join(t.TempDir(), "messages"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(ril.New(c, ""), st, Settings{Lines: []string{first, second}, Line: 0}, nil, nil)

	code, reply, err := s.Request(0, sit.SetStandby, []byte{1})
	if err != nil || code != 0 || !bytes.Equal(reply, []byte{7, 8}) {
		t.Fatalf("line 0 = %d %v %v", code, reply, err)
	}
	code, reply, err = s.Request(1, 0x0999, []byte{1, 2, 3})
	if err != nil || code != 6 || !bytes.Equal(reply, []byte{9}) {
		t.Fatalf("a refusal on line 1 = %d %v %v, want code 6 with its reply", code, reply, err)
	}
	if _, _, err := s.Request(2, sit.SetStandby, nil); err == nil {
		t.Fatal("a line the modem does not have was accepted")
	}
	if got := firstSeen.got(); len(got) != 1 || got[0] != "SET_MODEMS_CONFIG 1" {
		t.Fatalf("line 0 saw %v", got)
	}
	if got := secondSeen.got(); len(got) != 1 || got[0] != "0x0999 3" {
		t.Fatalf("line 1 saw %v", got)
	}
}
