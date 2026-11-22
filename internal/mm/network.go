package mm

import (
	"errors"

	"github.com/godbus/dbus/v5"

	"github.com/singularityos-lab/sinty-modem/internal/phone"
)

const (
	networkPath = dbus.ObjectPath("/dev/sinty/Modem1/Network")
	ifNetwork   = "dev.sinty.Modem1.Network"
)

type NetworkControl interface {
	NetworkSettings() (data, roaming bool, apn string)
	IMEI() (string, error)
	DataUsage() (rx, tx uint64)
	SetData(on bool) error
	SetRoaming(on bool) error
	SetAPN(name string) error
}

func networkMethods(c NetworkControl) map[string]interface{} {
	return map[string]interface{}{
		"GetSettings": func() (bool, bool, string, *dbus.Error) {
			data, roaming, apn := c.NetworkSettings()
			return data, roaming, apn, nil
		},
		"GetUsage": func() (uint64, uint64, *dbus.Error) {
			rx, tx := c.DataUsage()
			return rx, tx, nil
		},
		"GetIMEI": func() (string, *dbus.Error) {
			imei, err := c.IMEI()
			if err != nil {
				return "", fail("Failed", err)
			}
			return imei, nil
		},
		"SetData": func(on bool) *dbus.Error {
			if err := c.SetData(on); err != nil {
				return fail("Failed", err)
			}
			return nil
		},
		"SetRoaming": func(on bool) *dbus.Error {
			if err := c.SetRoaming(on); err != nil {
				return fail("Failed", err)
			}
			return nil
		},
		"SetAPN": func(name string) *dbus.Error {
			if err := c.SetAPN(name); errors.Is(err, phone.ErrBadAPN) {
				return fail("InvalidArgs", err)
			} else if err != nil {
				return fail("Failed", err)
			}
			return nil
		},
	}
}

func ExportNetwork(conn *dbus.Conn, c NetworkControl) {
	b := NewBus(conn, networkPath, PolkitAuthorizer(conn))
	b.Add(networkPath, map[string]props{ifNetwork: {}}, map[string]map[string]interface{}{ifNetwork: networkMethods(c)})
}
