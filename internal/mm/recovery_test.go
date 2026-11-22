package mm

import (
	"bufio"
	"os/exec"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

type idleStack struct{ called bool }

func (s *idleStack) Restart() error      { s.called = true; return nil }
func (s *idleStack) Diagnostics() string { s.called = true; return "log" }

func TestStackRecoveryNeedsAuthorization(t *testing.T) {
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
	server, err := dbus.Connect(strings.TrimSpace(addr))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := dbus.Connect(strings.TrimSpace(addr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var uid uint32
	if err := client.BusObject().Call("org.freedesktop.DBus.GetConnectionUnixUser", 0, client.Names()[0]).Store(&uid); err != nil {
		t.Fatal(err)
	}
	if uid == 0 {
		t.Skip("root is always authorized")
	}
	s := &idleStack{}
	if err := ExportRecovery(server, s); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"Restart", "Diagnostics"} {
		err := client.Object(RecoveryName, recoveryPath).Call(ifStack+"."+method, 0).Err
		if err == nil || !strings.Contains(err.Error(), actionDeviceControl) || s.called {
			t.Fatalf("%s went through without polkit: %v", method, err)
		}
	}
}
