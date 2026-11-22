package mm

import (
	"errors"
	"fmt"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/singularityos-lab/sinty-modem/internal/phone"
)

type refusingNetwork struct {
	NetworkControl
	err error
}

func (n refusingNetwork) SetAPN(string) error { return n.err }

func TestAPNRefusalKeepsItsReason(t *testing.T) {
	for want, err := range map[string]error{
		errCore + "Failed":      errors.New("end the current call first"),
		errCore + "InvalidArgs": fmt.Errorf("APN: %w", phone.ErrBadAPN),
	} {
		set := networkMethods(refusingNetwork{err: err})["SetAPN"].(func(string) *dbus.Error)
		if got := set("internet"); got == nil || got.Name != want {
			t.Errorf("%v: got %v, want %s", err, got, want)
		}
	}
}
