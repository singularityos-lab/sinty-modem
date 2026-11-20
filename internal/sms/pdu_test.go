package sms

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestReceivedReferencePDU(t *testing.T) {
	raw, _ := hex.DecodeString("07911326040000F0040B911346610089F60000208062917314080CC8F71D14969741F977FD07")
	m, err := Received(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Number != "+31641600986" || m.Text != "How are you?" {
		t.Fatalf("got %q %q", m.Number, m.Text)
	}
	if m.Timestamp.Year() != 2002 || m.Timestamp.Month() != 8 || m.Timestamp.Day() != 26 {
		t.Fatalf("timestamp %v", m.Timestamp)
	}
}

func deliverOf(submit []byte) []byte {
	n := int(submit[2])
	addr := submit[2 : 4+(n+1)/2]
	rest := submit[4+(n+1)/2:]
	d := []byte{submit[0] &^ 3 & 0x40}
	d = append(d, addr...)
	d = append(d, rest[0], rest[1])
	d = append(d, 0x62, 0x11, 0x02, 0x21, 0x43, 0x00, 0x40)
	return append(d, rest[2:]...)
}

func TestRoundTrip(t *testing.T) {
	for _, text := range []string{"Ciao, è già tardi? {€}", "Привет мир", strings.Repeat("abcdefghij", 40), strings.Repeat("😀", 100)} {
		parts, err := Submit("+393331234567", text)
		if err != nil {
			t.Fatal(err)
		}
		var got strings.Builder
		for i, p := range parts {
			m, err := Deliver(deliverOf(p))
			if err != nil {
				t.Fatalf("%q part %d: %v", text, i, err)
			}
			if m.Number != "+393331234567" {
				t.Fatalf("number %q", m.Number)
			}
			if len(parts) > 1 && (m.Concat == nil || m.Concat.Seq != i+1 || m.Concat.Total != len(parts)) {
				t.Fatalf("concat %+v", m.Concat)
			}
			got.WriteString(m.Text)
		}
		if got.String() != text {
			t.Fatalf("round trip %q -> %q", text, got.String())
		}
	}
}
