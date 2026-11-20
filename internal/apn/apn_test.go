package apn

import (
	"os"
	"testing"
)

func TestItalianCarriers(t *testing.T) {
	path := os.Getenv("SINTY_APN_DB")
	if path == "" {
		t.Skip("set SINTY_APN_DB to serviceproviders.xml")
	}
	for _, c := range []struct{ mcc, mnc string }{{"222", "01"}, {"222", "10"}, {"222", "88"}, {"222", "50"}, {"310", "260"}} {
		car, err := Lookup(path, c.mcc, []string{c.mnc})
		if err != nil {
			t.Fatal(err)
		}
		if car == nil {
			t.Errorf("%s%s: none", c.mcc, c.mnc)
			continue
		}
		t.Logf("%s%s: %+v", c.mcc, c.mnc, *car)
	}
}
