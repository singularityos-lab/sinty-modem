package mm

import (
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestActionFor(t *testing.T) {
	cases := []struct{ iface, method, want string }{
		{ifVoice, "CreateCall", actionVoice},
		{ifCall, "Start", actionVoice},
		{ifCallAudio, "SetMute", actionVoice},
		{ifVoice, "ListCalls", ""},
		{ifMessaging, "Create", actionMessaging},
		{ifSms, "Send", actionMessaging},
		{ifMessaging, "List", ""},
		{ifSim, "SendPin", actionDeviceControl},
		{ifModem, "Enable", actionDeviceControl},
		{ifModem, "ListBearers", ""},
		{ifManager, "SetLogging", actionControl},
	}
	for _, c := range cases {
		if got := actionFor(c.iface, c.method); got != c.want {
			t.Errorf("actionFor(%s, %s) = %q, want %q", c.iface, c.method, got, c.want)
		}
	}
}

func TestGuardOverTheBus(t *testing.T) {
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
	buf := make([]byte, 512)
	n, err := out.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(string(buf[:n]))

	server, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var mu sync.Mutex
	allow := map[string]bool{}
	var seen []string
	auth := func(sender dbus.Sender, action string) *dbus.Error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, string(sender))
		if allow[action] {
			return nil
		}
		return unauthorized(action)
	}
	b := NewBus(server, "/test", auth)
	dialed := ""
	b.Add("/test/Modem", map[string]props{ifVoice: {}}, map[string]map[string]interface{}{ifVoice: {
		"CreateCall": func(p map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
			mu.Lock()
			defer mu.Unlock()
			dialed = p["number"].Value().(string)
			return "/test/Call/1", nil
		},
		"ListCalls": func() ([]dbus.ObjectPath, *dbus.Error) { return []dbus.ObjectPath{"/test/Call/1"}, nil },
	}})

	obj := client.Object(server.Names()[0], "/test/Modem")
	args := map[string]dbus.Variant{"number": dbus.MakeVariant("112")}

	var path dbus.ObjectPath
	err = obj.Call(ifVoice+".CreateCall", 0, args).Store(&path)
	mu.Lock()
	gotDialed, gotSeen := dialed, seen
	mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), actionVoice) || gotDialed != "" {
		t.Fatalf("denied call went through: err=%v dialed=%q", err, gotDialed)
	}
	if len(gotSeen) != 1 || gotSeen[0] != client.Names()[0] {
		t.Fatalf("authorizer saw sender %v, want %s", gotSeen, client.Names()[0])
	}

	var calls []dbus.ObjectPath
	if err := obj.Call(ifVoice+".ListCalls", 0).Store(&calls); err != nil || len(calls) != 1 {
		t.Fatalf("open method failed: %v %v", err, calls)
	}

	mu.Lock()
	allow[actionVoice] = true
	mu.Unlock()
	err = obj.Call(ifVoice+".CreateCall", 0, args).Store(&path)
	mu.Lock()
	defer mu.Unlock()
	if err != nil || path != "/test/Call/1" || dialed != "112" {
		t.Fatalf("allowed call failed: err=%v path=%s dialed=%q", err, path, dialed)
	}
}

func TestRecoveryAsksForDeviceControl(t *testing.T) {
	for _, method := range []string{"SelectSlot", "SetStandby", "SetDataLine", "Request", "Restart"} {
		if got := actionFor(ifRecovery, method); got != actionDeviceControl {
			t.Fatalf("%s asks for %q", method, got)
		}
	}
}
