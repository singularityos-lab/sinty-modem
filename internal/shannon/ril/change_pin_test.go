package ril

import (
	"bytes"
	"testing"
)

func TestChangePINPayloadCarriesBothCodes(t *testing.T) {
	aid := []byte{0xa0, 0, 0, 0, 0x87, 0x10, 0x02}
	got, err := changePINPayload("1234", "567890", aid)
	want := []byte{3, 4, '1', '2', '3', '4', 0, 0, 0, 0, 6, '5', '6', '7', '8', '9', '0', 0, 0, 7, 0xa0, 0, 0, 0, 0x87, 0x10, 0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got % x, error %v", got, err)
	}
	for _, c := range [][2]string{{"123", "5678"}, {"1234", "56a8"}} {
		if _, err := changePINPayload(c[0], c[1], aid); err == nil {
			t.Errorf("accepted %v", c)
		}
	}
}
