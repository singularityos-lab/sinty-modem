package ril

import (
	"bytes"
	"testing"
)

func TestNetworkSearchIsRead(t *testing.T) {
	p := []byte{2, 0, 0, 0,
		14, 0, 0, 0, '0', '0', '1', '0', '1', '#', 2, 0, 0, 0,
		3, 0, 0, 0, '9', '9', '9', '7', '0', '2', 3, 0, 0, 0}
	got, err := decodeNetworks(p)
	want := []Network{{PLMN: "00101", Tech: 14, Status: 2}, {PLMN: "999702", Tech: 3, Status: 3}}
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, error %v", got, err)
	}
	for _, bad := range [][]byte{nil, p[:20], {255, 255, 255, 255}} {
		if _, err := decodeNetworks(bad); err == nil {
			t.Errorf("% x was accepted", bad)
		}
	}
}

func TestManualSelectionPadsAShortPLMN(t *testing.T) {
	got, err := selectionPayload(Network{PLMN: "00101", Tech: 14})
	if err != nil || !bytes.Equal(got, []byte{14, 0, 0, 0, '0', '0', '1', '0', '1', '#'}) {
		t.Fatalf("got % x, error %v", got, err)
	}
	for _, bad := range []string{"", "0010", "0010a", "0010100"} {
		if _, err := selectionPayload(Network{PLMN: bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
