package shannon

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRadioOffSurvivesARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "network.json")
	p := loadPrefs(file)
	p.RadioOff = true
	if err := savePrefs(file, p); err != nil {
		t.Fatal(err)
	}
	if !New(nil, nil, Settings{PrefsFile: file}, nil, nil).radioOff {
		t.Fatal("the radio would come back on after a restart")
	}
}

func TestBringUpWaitsUntilTheRadioIsWanted(t *testing.T) {
	s := &Service{radioOff: true, simWake: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() { done <- s.awaitRadio(context.Background()) }()
	s.wake()
	select {
	case <-done:
		t.Fatal("went on with the radio switched off")
	case <-time.After(50 * time.Millisecond):
	}
	s.mu.Lock()
	s.radioOff = false
	s.mu.Unlock()
	s.wake()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("did not go on")
	}
}
