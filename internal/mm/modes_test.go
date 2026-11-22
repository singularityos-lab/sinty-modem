package mm

import "testing"

func TestOnlyTheOfferedModesAreAccepted(t *testing.T) {
	for m, want := range map[modes]bool{{mode4G, 0}: true, {mode2G | mode3G | mode4G, mode4G}: false} {
		if got, ok := lteOnlyMode(m); !ok || got != want {
			t.Errorf("%v: got %v %v", m, got, ok)
		}
		if currentModes(want) != m {
			t.Errorf("%v is not reported back", m)
		}
	}
	for _, m := range []modes{{mode3G, 0}, {16, 0}, {}} {
		if _, ok := lteOnlyMode(m); ok {
			t.Errorf("accepted %v", m)
		}
	}
}
