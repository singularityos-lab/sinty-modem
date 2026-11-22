package esim

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/damonto/euicc-go/driver"
)

const testICCID = "8988201000000000001"

type card struct {
	t       *testing.T
	enabled bool
	open    int
	sent    [][]byte
	broken  bool
}

func (c *card) Connect() error    { return nil }
func (c *card) Disconnect() error { return nil }

func (c *card) OpenLogicalChannel([]byte) (byte, error) {
	if c.broken {
		return 0, errors.New("no logical channel")
	}
	c.open++
	return 1, nil
}

func (c *card) CloseLogicalChannel(byte) error {
	c.open--
	return nil
}

func tlv(tag string, parts ...[]byte) []byte {
	b, _ := hex.DecodeString(tag)
	v := bytes.Join(parts, nil)
	return append(append(b, byte(len(v))), v...)
}

func (c *card) Transmit(cmd []byte) ([]byte, error) {
	if len(cmd) < 7 || cmd[0] != 0x81 || cmd[1] != 0xe2 {
		c.t.Fatalf("unexpected command % x", cmd)
	}
	body := cmd[5:]
	c.sent = append(c.sent, body)
	var out []byte
	switch body[1] {
	case 0x3e:
		out = tlv("bf3e", tlv("5a", bytes.Repeat([]byte{0x89}, 16)))
	case 0x2d:
		var state byte
		if c.enabled {
			state = 1
		}
		iccid, _ := hex.DecodeString("988802010000000000f1")
		out = tlv("bf2d", tlv("a0", tlv("e3", tlv("5a", iccid),
			tlv("4f", []byte{0xa0, 0, 0, 5, 0x59, 0x10, 0x10, 0xff, 0xff, 0xff, 0xff, 0x89, 0, 0, 0x11, 0}),
			tlv("9f70", []byte{state}), tlv("91", []byte("Carrier")), tlv("92", []byte("Plan")))))
	case 0x31, 0x32, 0x33:
		c.enabled = body[1] == 0x31
		out = tlv("bf"+hex.EncodeToString(body[1:2]), tlv("80", []byte{0}))
	case 0x28:
		out = tlv("bf28", tlv("a0"))
	default:
		c.t.Fatalf("unexpected request % x", body)
	}
	return append(out, 0x90, 0), nil
}

func (c *card) asked(tag byte) bool {
	for _, b := range c.sent {
		if b[1] == tag {
			return true
		}
	}
	return false
}

func manager(c *card, changed *int) *Manager {
	return New(func() driver.SmartCardChannel { return c },
		func() (string, error) { return "", errors.New("no IMEI") },
		func() { *changed++ })
}

func TestProfilesAreListed(t *testing.T) {
	var changed int
	c := &card{t: t, enabled: true}
	m := manager(c, &changed)
	got, err := m.Profiles()
	if err != nil || len(got) != 1 {
		t.Fatalf("profiles %v %v", got, err)
	}
	if got[0] != (Profile{ICCID: testICCID, Name: "Plan", Provider: "Carrier", Enabled: true}) {
		t.Fatalf("profile %+v", got[0])
	}
	if eid, err := m.EID(); err != nil || len(eid) != 32 {
		t.Fatalf("eid %q %v", eid, err)
	}
	if c.open != 0 {
		t.Fatal("a channel was left open")
	}
}

func TestEnablingReloadsTheSIM(t *testing.T) {
	var changed int
	c := &card{t: t}
	m := manager(c, &changed)
	if err := m.Enable(testICCID); err != nil {
		t.Fatal(err)
	}
	if !c.enabled || changed != 1 || !c.asked(0x28) || c.open != 0 {
		t.Fatalf("enabled %t changed %d open %d", c.enabled, changed, c.open)
	}
	if err := m.Enable(testICCID); err != nil || changed != 1 {
		t.Fatalf("enabling twice restarted the modem: %v", err)
	}
	if err := m.Disable(testICCID); err != nil || c.enabled || changed != 2 {
		t.Fatalf("disable %v", err)
	}
}

func TestTheActiveProfileIsNotDeleted(t *testing.T) {
	var changed int
	c := &card{t: t, enabled: true}
	m := manager(c, &changed)
	if err := m.Delete(testICCID); !errors.Is(err, ErrActive) || c.asked(0x33) {
		t.Fatalf("the active profile was deleted: %v", err)
	}
	if err := m.Delete("8988201000000000002"); !errors.Is(err, ErrNoProfile) || c.asked(0x33) {
		t.Fatalf("an unknown profile was deleted: %v", err)
	}
}

func TestNoEUICCIsReported(t *testing.T) {
	var changed int
	if _, err := manager(&card{t: t, broken: true}, &changed).Profiles(); !errors.Is(err, ErrNoEUICC) {
		t.Fatalf("got %v", err)
	}
}

func TestActivationCodes(t *testing.T) {
	for in, want := range map[string]string{
		"LPA:1$rsp.example.com$ABC-123":     "rsp.example.com",
		" 1$rsp.example.com:443$ABC-123\n ": "rsp.example.com:443",
	} {
		if ac, err := activationCode(in, ""); err != nil || ac.SMDP.Host != want || ac.MatchingID != "ABC-123" {
			t.Errorf("%q: %v", in, err)
		}
	}
	for _, in := range []string{"", "LPA:2$rsp.example.com$A", "LPA:1$$A", "LPA:1$user@rsp.example.com$A", "LPA:1$rsp.example.com/path$A", "https://rsp.example.com"} {
		if _, err := activationCode(in, ""); !errors.Is(err, ErrBadCode) {
			t.Errorf("%q was accepted", in)
		}
	}
	if _, err := activationCode("LPA:1$rsp.example.com$A$$1", ""); !errors.Is(err, ErrConfirmation) {
		t.Errorf("confirmation code not asked: %v", err)
	}
}
