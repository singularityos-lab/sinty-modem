package ril

import (
	"bytes"
	"testing"
)

func TestPUKPayloadCarriesBothCodesAndTheApp(t *testing.T) {
	aid := []byte{0xa0, 0, 0, 0, 0x87, 0x10, 0x02}
	got, err := pukPayload("12345678", "4321", aid)
	want := []byte{8, '1', '2', '3', '4', '5', '6', '7', '8', 4, '4', '3', '2', '1', 0, 0, 0, 0, 7, 0xa0, 0, 0, 0, 0x87, 0x10, 0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got % x, error %v", got, err)
	}
	for _, c := range [][2]string{{"1234567", "4321"}, {"1234567a", "4321"}, {"12345678", "43"}} {
		if _, err := pukPayload(c[0], c[1], aid); err == nil {
			t.Errorf("accepted %v", c)
		}
	}
}
