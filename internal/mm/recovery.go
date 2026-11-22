package mm

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	RecoveryName = "dev.sinty.Modem1"
	recoveryPath = dbus.ObjectPath("/dev/sinty/Modem1")
	ifStack      = "dev.sinty.Modem1.Stack"
)

type StackControl interface {
	Restart() error
	Diagnostics() string
}

func ExportRecovery(conn *dbus.Conn, s StackControl) error {
	b := NewBus(conn, recoveryPath, PolkitAuthorizer(conn))
	b.Add(recoveryPath, map[string]props{ifStack: {}}, map[string]map[string]interface{}{
		ifStack: {
			"Restart": func() *dbus.Error {
				if err := s.Restart(); err != nil {
					return fail("Failed", err)
				}
				return nil
			},
			"Diagnostics": func() (string, *dbus.Error) { return s.Diagnostics(), nil },
		},
	})
	reply, err := conn.RequestName(RecoveryName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("%s is already owned by another service", RecoveryName)
	}
	return nil
}
