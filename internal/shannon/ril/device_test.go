package ril

import (
	"strings"
	"testing"
)

func TestIMEIIsReadFromItsLengthPrefix(t *testing.T) {
	digits := strings.Repeat("0", 15)
	if got, err := decodeIMEI(append([]byte{15}, digits+"\x00\x00"...)); err != nil || got != digits {
		t.Fatalf("got %q %v", got, err)
	}
	for _, p := range [][]byte{nil, {15, '1'}, append([]byte{15}, strings.Repeat("x", 15)...)} {
		if _, err := decodeIMEI(p); err == nil {
			t.Errorf("% x was accepted", p)
		}
	}
}
