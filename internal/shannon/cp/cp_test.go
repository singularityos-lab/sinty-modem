package cp

import (
	"os"
	"testing"
)

func TestBasebandVersionOfARealImage(t *testing.T) {
	path := os.Getenv("SINTY_MODEM_BIN")
	if path == "" {
		t.Skip("set SINTY_MODEM_BIN to a modem.bin from a factory image")
	}
	v, err := BasebandVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(v)
	if v == "" {
		t.Fatal("empty version")
	}
}
