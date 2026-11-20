package mm

import (
	"errors"
	"sort"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const (
	propsIface = "org.freedesktop.DBus.Properties"
	omIface    = "org.freedesktop.DBus.ObjectManager"
)

type props map[string]dbus.Variant

type object struct {
	path    dbus.ObjectPath
	ifaces  map[string]props
	methods map[string]map[string]interface{}
}

type Bus struct {
	conn *dbus.Conn
	root dbus.ObjectPath

	mu      sync.Mutex
	objects map[dbus.ObjectPath]*object
}

func NewBus(conn *dbus.Conn, root dbus.ObjectPath) *Bus {
	b := &Bus{conn: conn, root: root, objects: map[dbus.ObjectPath]*object{}}
	conn.ExportMethodTable(map[string]interface{}{
		"GetManagedObjects": b.getManagedObjects,
	}, root, omIface)
	return b
}

func (b *Bus) getManagedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, *dbus.Error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{}
	for p, o := range b.objects {
		if p == b.root {
			continue
		}
		out[p] = o.snapshot()
	}
	return out, nil
}

func (o *object) snapshot() map[string]map[string]dbus.Variant {
	out := map[string]map[string]dbus.Variant{}
	for name, pr := range o.ifaces {
		m := map[string]dbus.Variant{}
		for k, v := range pr {
			m[k] = v
		}
		out[name] = m
	}
	return out
}

type propsHandler struct {
	b    *Bus
	path dbus.ObjectPath
}

func (h propsHandler) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	o := h.b.objects[h.path]
	if o == nil {
		return dbus.Variant{}, dbus.MakeFailedError(errNoObject)
	}
	pr, ok := o.ifaces[iface]
	if !ok {
		return dbus.Variant{}, &dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownInterface", Body: []interface{}{iface}}
	}
	v, ok := pr[name]
	if !ok {
		return dbus.Variant{}, &dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownProperty", Body: []interface{}{name}}
	}
	return v, nil
}

func (h propsHandler) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	o := h.b.objects[h.path]
	if o == nil {
		return nil, dbus.MakeFailedError(errNoObject)
	}
	out := map[string]dbus.Variant{}
	for k, v := range o.ifaces[iface] {
		out[k] = v
	}
	return out, nil
}

func (h propsHandler) Set(iface, name string, v dbus.Variant) *dbus.Error {
	return &dbus.Error{Name: "org.freedesktop.DBus.Error.PropertyReadOnly", Body: []interface{}{name}}
}

func (b *Bus) Add(path dbus.ObjectPath, ifaces map[string]props, methods map[string]map[string]interface{}) {
	o := &object{path: path, ifaces: ifaces, methods: methods}
	b.mu.Lock()
	b.objects[path] = o
	snap := o.snapshot()
	b.mu.Unlock()
	for iface, table := range methods {
		b.conn.ExportMethodTable(table, path, iface)
	}
	h := propsHandler{b: b, path: path}
	b.conn.ExportMethodTable(map[string]interface{}{"Get": h.Get, "GetAll": h.GetAll, "Set": h.Set}, path, propsIface)
	b.conn.Export(introspect.NewIntrospectable(b.introspection(path)), path, "org.freedesktop.DBus.Introspectable")
	if path != b.root {
		b.conn.Emit(b.root, omIface+".InterfacesAdded", path, snap)
	}
}

func (b *Bus) Remove(path dbus.ObjectPath) {
	b.mu.Lock()
	o := b.objects[path]
	delete(b.objects, path)
	b.mu.Unlock()
	if o == nil {
		return
	}
	var names []string
	for iface := range o.ifaces {
		names = append(names, iface)
		b.conn.Export(nil, path, iface)
	}
	b.conn.Export(nil, path, propsIface)
	b.conn.Export(nil, path, "org.freedesktop.DBus.Introspectable")
	sort.Strings(names)
	if path != b.root {
		b.conn.Emit(b.root, omIface+".InterfacesRemoved", path, names)
	}
}

func (b *Bus) Update(path dbus.ObjectPath, iface string, changes props) {
	b.mu.Lock()
	o := b.objects[path]
	if o == nil {
		b.mu.Unlock()
		return
	}
	pr := o.ifaces[iface]
	diff := map[string]dbus.Variant{}
	for k, v := range changes {
		if old, ok := pr[k]; ok && old.String() == v.String() && old.Signature() == v.Signature() {
			continue
		}
		pr[k] = v
		diff[k] = v
	}
	b.mu.Unlock()
	if len(diff) > 0 {
		b.conn.Emit(path, propsIface+".PropertiesChanged", iface, diff, []string{})
	}
}

func (b *Bus) Emit(path dbus.ObjectPath, iface, member string, args ...interface{}) {
	b.conn.Emit(path, iface+"."+member, args...)
}

func (b *Bus) introspection(path dbus.ObjectPath) *introspect.Node {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := &introspect.Node{Name: string(path)}
	n.Interfaces = append(n.Interfaces, introspect.IntrospectData, introspect.Interface{Name: propsIface})
	o := b.objects[path]
	if o == nil {
		return n
	}
	var names []string
	for iface := range o.ifaces {
		names = append(names, iface)
	}
	sort.Strings(names)
	for _, iface := range names {
		in := introspect.Interface{Name: iface}
		for m := range o.methods[iface] {
			in.Methods = append(in.Methods, introspect.Method{Name: m})
		}
		for k, v := range o.ifaces[iface] {
			in.Properties = append(in.Properties, introspect.Property{Name: k, Type: v.Signature().String(), Access: "read"})
		}
		n.Interfaces = append(n.Interfaces, in)
	}
	if path == b.root {
		n.Interfaces = append(n.Interfaces, introspect.Interface{Name: omIface})
	}
	return n
}

var errNoObject = errors.New("no such object")
