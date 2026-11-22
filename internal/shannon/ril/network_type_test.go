package ril

import (
	"bytes"
	"testing"
)

func TestNetworkTypePayload(t *testing.T) {
	for lteOnly, want := range map[bool][]byte{true: {0x80, 0, 0, 0}, false: {0xfe, 0x07, 0, 0}} {
		if got := networkTypePayload(lteOnly); !bytes.Equal(got, want) {
			t.Errorf("4G only %v: got % x", lteOnly, got)
		}
	}
}
