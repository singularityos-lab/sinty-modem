package mm

import (
	"bufio"
	"os/exec"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/singularityos-lab/sinty-modem/internal/esim"
)

type fakeProfiles struct{ enabled string }

func (f *fakeProfiles) EID() (string, error) { return "EID", nil }
func (f *fakeProfiles) Profiles() ([]esim.Profile, error) {
	return []esim.Profile{{ICCID: "one", Name: "Plan", Provider: "Carrier", Enabled: true}}, nil
}
func (f *fakeProfiles) Enable(id string) error   { f.enabled = id; return nil }
func (f *fakeProfiles) Disable(string) error     { return nil }
func (f *fakeProfiles) Rename(_, _ string) error { return nil }
func (f *fakeProfiles) Delete(string) error      { return esim.ErrActive }
func (f *fakeProfiles) Install(_, _ string) error {
	return esim.ErrConfirmation
}
func (f *fakeProfiles) SendNotifications() error { return nil }

func TestESIMProfilesOverTheBus(t *testing.T) {
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

	f := &fakeProfiles{}
	exportESIM(server, f, nil)
	o := client.Object(server.Names()[0], esimPath)

	var list []esim.Profile
	call := o.Call(ifESIM+".ListProfiles", 0)
	if err := call.Store(&list); err != nil || len(list) != 1 || list[0].Provider != "Carrier" {
		t.Fatalf("profiles %v %v", list, err)
	}
	if sig := dbus.SignatureOf(list).String(); sig != "a(ssssb)" {
		t.Fatalf("signature %s", sig)
	}
	if err := o.Call(ifESIM+".EnableProfile", 0, "one").Err; err != nil || f.enabled != "one" {
		t.Fatalf("enable %v", err)
	}
	for method, want := range map[string]string{"DeleteProfile": "Active", "AddProfile": "ConfirmationNeeded"} {
		args := []interface{}{"one"}
		if method == "AddProfile" {
			args = append(args, "")
		}
		err, ok := o.Call(ifESIM+"."+method, 0, args...).Err.(dbus.Error)
		if !ok || err.Name != errESIM+want {
			t.Fatalf("%s: %v", method, err)
		}
	}
}

func TestESIMNeedsDeviceControl(t *testing.T) {
	for method := range esimMethods(&fakeProfiles{}) {
		if got := actionFor(ifESIM, method); got != actionDeviceControl {
			t.Errorf("%s asks for %q", method, got)
		}
	}
}
