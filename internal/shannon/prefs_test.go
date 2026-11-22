package shannon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/phone"
	"github.com/singularityos-lab/sinty-modem/internal/shannon/ril"
)

func TestNetworkChoicesSurviveARestart(t *testing.T) {
	_, s := startWithoutSIM(t, time.Hour)
	s.settings.PrefsFile = filepath.Join(t.TempDir(), "network")
	if p := loadPrefs(s.settings.PrefsFile); !p.Data || !p.Roaming || p.APN != "" {
		t.Fatalf("defaults %+v", p)
	}
	if err := s.SetData(false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoaming(false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAPN("internet.example"); err != nil {
		t.Fatal(err)
	}
	if p := loadPrefs(s.settings.PrefsFile); p.Data || p.Roaming || p.APN != "internet.example" {
		t.Fatalf("saved %+v", p)
	}
	for _, bad := range []string{"two words", "semi;colon", "-dash"} {
		if err := s.SetAPN(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestTurningDataOffDropsTheConnection(t *testing.T) {
	_, s := startWithoutSIM(t, time.Hour)
	b := make(ril.Bearer, 132)
	s.mu.Lock()
	s.internet = b
	s.mu.Unlock()
	s.update(func(x *phone.Status) { x.Stage, x.Radio, x.Data = phone.StageReady, true, true })
	if err := s.SetData(false); err != nil {
		t.Fatal(err)
	}
	if s.Status().Data {
		t.Fatal("data stayed up")
	}
}

func TestDataWaitsWhileRoamingIsOff(t *testing.T) {
	_, s := startWithoutSIM(t, time.Hour)
	s.update(func(x *phone.Status) { x.Registration = "roaming" })
	if !s.dataWanted() {
		t.Fatal("roaming is allowed by default")
	}
	if err := s.SetRoaming(false); err != nil {
		t.Fatal(err)
	}
	if s.dataWanted() {
		t.Fatal("data would start while roaming is off")
	}
	s.update(func(x *phone.Status) { x.Registration = "home" })
	if !s.dataWanted() {
		t.Fatal("the roaming switch blocked the home network")
	}
}
