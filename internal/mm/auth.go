package mm

import (
	"reflect"

	"github.com/godbus/dbus/v5"
)

const (
	actionControl       = "org.freedesktop.ModemManager1.Control"
	actionDeviceControl = "org.freedesktop.ModemManager1.Device.Control"
	actionVoice         = "org.freedesktop.ModemManager1.Voice"
	actionMessaging     = "org.freedesktop.ModemManager1.Messaging"
)

type Authorizer func(sender dbus.Sender, action string) *dbus.Error

var openMethods = map[string]bool{
	"ListBearers": true,
	"ListCalls":   true,
	"List":        true,
}

func actionFor(iface, method string) string {
	if openMethods[method] {
		return ""
	}
	switch iface {
	case ifManager:
		return actionControl
	case ifVoice, ifCall:
		return actionVoice
	case ifMessaging, ifSms:
		return actionMessaging
	}
	return actionDeviceControl
}

func unauthorized(action string) *dbus.Error {
	return &dbus.Error{Name: errCore + "Unauthorized", Body: []interface{}{"not authorized for " + action}}
}

func PolkitAuthorizer(conn *dbus.Conn) Authorizer {
	return func(sender dbus.Sender, action string) *dbus.Error {
		var uid uint32
		if err := conn.BusObject().Call("org.freedesktop.DBus.GetConnectionUnixUser", 0, string(sender)).Store(&uid); err != nil {
			return unauthorized(action)
		}
		if uid == 0 {
			return nil
		}
		subject := struct {
			Kind    string
			Details map[string]dbus.Variant
		}{"system-bus-name", map[string]dbus.Variant{"name": dbus.MakeVariant(string(sender))}}
		var result struct {
			Authorized bool
			Challenge  bool
			Details    map[string]string
		}
		authority := conn.Object("org.freedesktop.PolicyKit1", "/org/freedesktop/PolicyKit1/Authority")
		call := authority.Call("org.freedesktop.PolicyKit1.Authority.CheckAuthorization", 0, subject, action, map[string]string{}, uint32(1), "")
		if call.Err != nil || call.Store(&result) != nil || !result.Authorized {
			return unauthorized(action)
		}
		return nil
	}
}

var (
	senderType = reflect.TypeOf(dbus.Sender(""))
	errorType  = reflect.TypeOf((*dbus.Error)(nil))
)

func guard(method interface{}, action string, auth Authorizer) interface{} {
	fn := reflect.ValueOf(method)
	t := fn.Type()
	if t.Kind() != reflect.Func || t.NumOut() == 0 || t.Out(t.NumOut()-1) != errorType {
		return method
	}
	in := []reflect.Type{senderType}
	for i := 0; i < t.NumIn(); i++ {
		in = append(in, t.In(i))
	}
	out := make([]reflect.Type, t.NumOut())
	for i := range out {
		out[i] = t.Out(i)
	}
	wrapped := reflect.FuncOf(in, out, t.IsVariadic())
	return reflect.MakeFunc(wrapped, func(args []reflect.Value) []reflect.Value {
		if err := auth(dbus.Sender(args[0].String()), action); err != nil {
			res := make([]reflect.Value, len(out))
			for i := range out[:len(out)-1] {
				res[i] = reflect.Zero(out[i])
			}
			res[len(out)-1] = reflect.ValueOf(err)
			return res
		}
		if t.IsVariadic() {
			return fn.CallSlice(args[1:])
		}
		return fn.Call(args[1:])
	}).Interface()
}
