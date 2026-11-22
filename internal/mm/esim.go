package mm

import (
	"errors"

	"github.com/godbus/dbus/v5"

	"github.com/singularityos-lab/sinty-modem/internal/esim"
)

const (
	esimPath = dbus.ObjectPath("/dev/sinty/Modem1/ESIM")
	ifESIM   = "dev.sinty.Modem1.ESIM"
	errESIM  = ifESIM + ".Error."
)

type ESIMControl interface {
	EID() (string, error)
	Profiles() ([]esim.Profile, error)
	Enable(iccid string) error
	Disable(iccid string) error
	Rename(iccid, name string) error
	Delete(iccid string) error
	Install(code, confirmation string) error
	SendNotifications() error
}

var esimErrors = []struct {
	err  error
	name string
}{
	{esim.ErrNoEUICC, "NoChip"},
	{esim.ErrNoProfile, "NoProfile"},
	{esim.ErrActive, "Active"},
	{esim.ErrBadCode, "InvalidCode"},
	{esim.ErrConfirmation, "ConfirmationNeeded"},
	{esim.ErrName, "InvalidName"},
}

func esimError(err error) *dbus.Error {
	if err == nil {
		return nil
	}
	name := "Failed"
	for _, e := range esimErrors {
		if errors.Is(err, e.err) {
			name = e.name
			break
		}
	}
	return &dbus.Error{Name: errESIM + name, Body: []interface{}{err.Error()}}
}

func esimMethods(c ESIMControl) map[string]interface{} {
	return map[string]interface{}{
		"GetEID": func() (string, *dbus.Error) {
			eid, err := c.EID()
			return eid, esimError(err)
		},
		"ListProfiles": func() ([]esim.Profile, *dbus.Error) {
			list, err := c.Profiles()
			if list == nil {
				list = []esim.Profile{}
			}
			return list, esimError(err)
		},
		"EnableProfile":  func(iccid string) *dbus.Error { return esimError(c.Enable(iccid)) },
		"DisableProfile": func(iccid string) *dbus.Error { return esimError(c.Disable(iccid)) },
		"RenameProfile":  func(iccid, name string) *dbus.Error { return esimError(c.Rename(iccid, name)) },
		"DeleteProfile":  func(iccid string) *dbus.Error { return esimError(c.Delete(iccid)) },
		"AddProfile": func(code, confirmation string) *dbus.Error {
			return esimError(c.Install(code, confirmation))
		},
		"SendNotifications": func() *dbus.Error { return esimError(c.SendNotifications()) },
	}
}

func ExportESIM(conn *dbus.Conn, c ESIMControl) {
	exportESIM(conn, c, PolkitAuthorizer(conn))
}

func exportESIM(conn *dbus.Conn, c ESIMControl, auth Authorizer) {
	b := NewBus(conn, esimPath, auth)
	b.Add(esimPath, map[string]props{ifESIM: {}}, map[string]map[string]interface{}{ifESIM: esimMethods(c)})
}
