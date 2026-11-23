package mm

import (
	"bufio"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/singularityos-lab/sinty-modem/internal/phone"
)

type grantAll struct {
	mu      sync.Mutex
	actions []string
}

type authResult struct {
	Authorized bool
	Challenge  bool
	Details    map[string]string
}

func (g *grantAll) CheckAuthorization(subject struct {
	Kind    string
	Details map[string]dbus.Variant
}, action string, details map[string]string, flags uint32, cancel string) (authResult, *dbus.Error) {
	g.mu.Lock()
	g.actions = append(g.actions, action)
	g.mu.Unlock()
	return authResult{Authorized: true}, nil
}

type recordingModem struct {
	phone.Backend
	mu       sync.Mutex
	got      []string
	busySlot bool
}

func (m *recordingModem) note(s string) {
	m.mu.Lock()
	m.got = append(m.got, s)
	m.mu.Unlock()
}

func (m *recordingModem) Status() phone.Status      { return phone.Status{} }
func (m *recordingModem) Calls() []phone.Call       { return nil }
func (m *recordingModem) Messages() []phone.Message { return nil }
func (m *recordingModem) Subscribe() (<-chan phone.Event, func()) {
	return make(chan phone.Event), func() {}
}

func (m *recordingModem) seen() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.got, ",")
}

func (m *recordingModem) SelectSlot(slot int) error {
	m.mu.Lock()
	busy := m.busySlot
	m.mu.Unlock()
	if busy {
		return errors.New("a call is in progress")
	}
	m.note("slot " + string(rune('0'+slot)))
	return nil
}

func (m *recordingModem) Restart() error { m.note("service restart"); return nil }

type recordingStack struct{ m *recordingModem }

func (s recordingStack) Restart() error      { s.m.note("modem restart"); return nil }
func (s recordingStack) Diagnostics() string { s.m.note("diagnostics"); return "stack log" }

func TestDeveloperRecoveryActionsReachTheModem(t *testing.T) {
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon not available")
	}
	daemon := exec.Command("dbus-daemon", "--session", "--nofork", "--print-address")
	out, err := daemon.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	defer daemon.Process.Kill()
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	conns := make([]*dbus.Conn, 3)
	for i := range conns {
		if conns[i], err = dbus.Connect(strings.TrimSpace(addr)); err != nil {
			t.Fatal(err)
		}
		defer conns[i].Close()
	}
	polkit, server, client := conns[0], conns[1], conns[2]

	grant := &grantAll{}
	if err := polkit.Export(grant, "/org/freedesktop/PolicyKit1/Authority", "org.freedesktop.PolicyKit1.Authority"); err != nil {
		t.Fatal(err)
	}
	if _, err := polkit.RequestName("org.freedesktop.PolicyKit1", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	modem := &recordingModem{}
	if _, err := Export(server, modem); err != nil {
		t.Fatal(err)
	}
	if err := ExportRecovery(server, recordingStack{modem}); err != nil {
		t.Fatal(err)
	}

	recovery := client.Object(Name, modemPath)
	stack := client.Object(RecoveryName, recoveryPath)
	calls := []struct {
		obj    dbus.BusObject
		method string
		args   []interface{}
	}{
		{stack, ifStack + ".Restart", nil},
		{recovery, ifRecovery + ".Restart", nil},
		{recovery, ifRecovery + ".SelectSlot", []interface{}{uint32(1)}},
		{recovery, ifRecovery + ".SelectSlot", []interface{}{uint32(0)}},
	}
	for _, c := range calls {
		if err := c.obj.Call(c.method, 0, c.args...).Err; err != nil {
			t.Fatalf("%s: %v", c.method, err)
		}
	}
	var log string
	if err := stack.Call(ifStack+".Diagnostics", 0).Store(&log); err != nil || log != "stack log" {
		t.Fatalf("diagnostics = %q, %v", log, err)
	}
	want := "modem restart,service restart,slot 1,slot 0,diagnostics"
	if got := modem.seen(); got != want {
		t.Fatalf("modem saw %s, want %s", got, want)
	}
	grant.mu.Lock()
	defer grant.mu.Unlock()
	for _, a := range grant.actions {
		if a != actionDeviceControl {
			t.Fatalf("asked polkit for %s, want %s", a, actionDeviceControl)
		}
	}
	var uid uint32
	if err := client.BusObject().Call("org.freedesktop.DBus.GetConnectionUnixUser", 0, client.Names()[0]).Store(&uid); err != nil {
		t.Fatal(err)
	}
	if asks := 5; uid == 0 && len(grant.actions) != 0 || uid != 0 && len(grant.actions) != asks {
		t.Fatalf("polkit asked %d times for uid %d", len(grant.actions), uid)
	}

	modem.mu.Lock()
	modem.busySlot = true
	modem.mu.Unlock()
	err = recovery.Call(ifRecovery+".SelectSlot", 0, uint32(1)).Err
	var dbusErr dbus.Error
	if !errors.As(err, &dbusErr) || !strings.Contains(dbusErr.Error(), "call is in progress") {
		t.Fatalf("a refused slot change reached the client as %v", err)
	}
}
