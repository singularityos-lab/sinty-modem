package recovery

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

type fakeInit struct {
	mu     sync.Mutex
	asked  []string
	silent bool
	state  string
}

func (f *fakeInit) listen(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atom")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("no unix sockets: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			f.answer(c)
		}
	}()
	return path
}

func (f *fakeInit) answer(c net.Conn) {
	defer c.Close()
	var size [4]byte
	if _, err := io.ReadFull(c, size[:]); err != nil {
		return
	}
	b := make([]byte, binary.BigEndian.Uint32(size[:]))
	if _, err := io.ReadFull(c, b); err != nil {
		return
	}
	var req request
	if json.Unmarshal(b, &req) != nil {
		return
	}
	f.mu.Lock()
	f.asked = append(f.asked, req.Cmd+" "+req.Unit)
	f.mu.Unlock()
	if f.silent && req.Cmd == "restart" {
		return
	}
	r := reply{OK: true, State: f.state}
	if req.Cmd == "logs" {
		r.Lines = []string{"call to +1 5550100", "eid 89AB12CD34EF56AB78CD90EF12AB34CD", "slot status (421 bytes, count 0)"}
	}
	out, _ := json.Marshal(r)
	binary.BigEndian.PutUint32(size[:], uint32(len(out)))
	c.Write(append(size[:], out...))
}

func TestRestartClearsTheLatchAndStartsBothUnits(t *testing.T) {
	for _, silent := range []bool{false, true} {
		f := &fakeInit{silent: silent, state: "active"}
		latch := filepath.Join(t.TempDir(), "attempts")
		if err := os.WriteFile(latch, []byte("3"), 0o600); err != nil {
			t.Fatal(err)
		}
		m := Modem{Socket: f.listen(t), Latch: latch}
		if err := m.Restart(); err != nil {
			t.Fatalf("silent %t: %v", silent, err)
		}
		if _, err := os.Stat(latch); !os.IsNotExist(err) {
			t.Fatal("the latch is still armed")
		}
		for _, want := range []string{"stop " + serviceUnit, "restart " + stackUnit, "restart " + serviceUnit} {
			if !slices.Contains(f.asked, want) {
				t.Fatalf("%q missing from %q", want, f.asked)
			}
		}
	}
}

func TestARestartThatDidNotComeUpIsReported(t *testing.T) {
	f := &fakeInit{silent: true, state: "failed"}
	if err := (Modem{Socket: f.listen(t), Latch: filepath.Join(t.TempDir(), "attempts")}).Restart(); err == nil {
		t.Fatal("a failed stack was reported as restarted")
	}
}

func TestDiagnosticsHideIdentifiers(t *testing.T) {
	f := &fakeInit{state: "failed"}
	text := Modem{Socket: f.listen(t), Latch: filepath.Join(t.TempDir(), "attempts")}.Diagnostics()
	if strings.Contains(text, "5550100") || strings.Contains(text, "89AB12CD") {
		t.Fatalf("an identifier leaked: %s", text)
	}
	if !strings.Contains(text, "421 bytes, count 0") || !strings.Contains(text, stackUnit+": failed") {
		t.Fatalf("the diagnostics lost the cause: %s", text)
	}
}
