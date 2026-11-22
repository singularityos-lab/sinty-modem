package shannon

import (
	"testing"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/phone"
)

func TestReconnectLeavesDataThatCameBackAlone(t *testing.T) {
	_, s := startWithoutSIM(t, time.Hour)
	s.update(func(x *phone.Status) { x.Stage, x.Radio, x.Data = phone.StageReady, true, true })
	if done, err := s.restoreData(); !done || err != nil {
		t.Fatalf("done %v, error %v", done, err)
	}
}
