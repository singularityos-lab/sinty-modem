package lp

import (
	"os"
	"testing"
)

func TestReadFactorySuperEmpty(t *testing.T) {
	b, err := os.ReadFile("testdata/super_empty.img")
	if err != nil {
		t.Fatal(err)
	}
	m, err := ReadImage(b)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, p := range m.Partitions {
		names[p.Name] = true
	}
	for _, want := range []string{"system_a", "vendor_a", "system_b", "vendor_b"} {
		if !names[want] {
			t.Errorf("no %s in %v", want, names)
		}
	}
	if len(m.BlockDevices) == 0 || m.BlockDevices[0].Name != "super" {
		t.Errorf("block devices %+v", m.BlockDevices)
	}
}

func TestChecksumIsEnforced(t *testing.T) {
	b, err := os.ReadFile("testdata/super_empty.img")
	if err != nil {
		t.Fatal(err)
	}
	b[geometrySize+200] ^= 0xff
	if _, err := ReadImage(b); err == nil {
		t.Fatal("a corrupted table was accepted")
	}
}
