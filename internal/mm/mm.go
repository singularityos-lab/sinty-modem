package mm

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/singularityos-lab/sinty-modem/internal/phone"
)

const (
	Name        = "org.freedesktop.ModemManager1"
	Root        = dbus.ObjectPath("/org/freedesktop/ModemManager1")
	modemPath   = dbus.ObjectPath("/org/freedesktop/ModemManager1/Modem/0")
	simPath     = dbus.ObjectPath("/org/freedesktop/ModemManager1/SIM/0")
	ifManager   = "org.freedesktop.ModemManager1"
	ifModem     = "org.freedesktop.ModemManager1.Modem"
	ifModem3gpp = "org.freedesktop.ModemManager1.Modem.Modem3gpp"
	ifVoice     = "org.freedesktop.ModemManager1.Modem.Voice"
	ifMessaging = "org.freedesktop.ModemManager1.Modem.Messaging"
	ifSignal    = "org.freedesktop.ModemManager1.Modem.Signal"
	ifSim       = "org.freedesktop.ModemManager1.Sim"
	ifCall      = "org.freedesktop.ModemManager1.Call"
	ifSms       = "org.freedesktop.ModemManager1.Sms"

	errCore = "org.freedesktop.ModemManager1.Error.Core."
)

const (
	callUnknown int32 = iota
	callDialing
	callRingingOut
	callRingingIn
	callActive
	callHeld
	callWaiting
	callTerminated
)

const (
	reasonUnknown int32 = iota
	reasonOutgoingStarted
	reasonIncomingNew
	reasonAccepted
	reasonTerminated
	reasonRefusedOrBusy
	reasonError
)

var callStates = map[phone.CallState]int32{
	phone.CallDialing: callDialing, phone.CallRingingOut: callRingingOut, phone.CallRingingIn: callRingingIn,
	phone.CallActive: callActive, phone.CallHeld: callHeld, phone.CallWaiting: callWaiting,
	phone.CallTerminated: callTerminated,
}

type callObj struct {
	path    dbus.ObjectPath
	id      string
	state   int32
	created bool
	number  string
	started bool
}

type smsObj struct {
	path   dbus.ObjectPath
	id     string
	number string
	text   string
	draft  bool
}

type Exporter struct {
	b       *Bus
	backend phone.Backend

	mu     sync.Mutex
	next   int
	calls  map[dbus.ObjectPath]*callObj
	byCall map[string]*callObj
	sms    map[dbus.ObjectPath]*smsObj
	bySMS  map[string]*smsObj
	state  int32
}

func fail(name string, err error) *dbus.Error {
	return &dbus.Error{Name: errCore + name, Body: []interface{}{err.Error()}}
}

func Export(conn *dbus.Conn, backend phone.Backend) (*Exporter, error) {
	e := &Exporter{
		b:       NewBus(conn, Root),
		backend: backend,
		calls:   map[dbus.ObjectPath]*callObj{},
		byCall:  map[string]*callObj{},
		sms:     map[dbus.ObjectPath]*smsObj{},
		bySMS:   map[string]*smsObj{},
	}
	none := func() *dbus.Error { return nil }
	e.b.Add(Root, map[string]props{ifManager: {"Version": dbus.MakeVariant("1.24.0-sinty")}}, map[string]map[string]interface{}{
		ifManager: {
			"ScanDevices":       none,
			"SetLogging":        func(string) *dbus.Error { return nil },
			"ReportKernelEvent": func(map[string]dbus.Variant) *dbus.Error { return nil },
			"InhibitDevice":     func(string, bool) *dbus.Error { return fail("Unsupported", errors.New("not supported")) },
		},
	})
	e.addSim()
	e.addModem()
	for _, m := range backend.Messages() {
		e.addMessage(m)
	}
	for _, c := range backend.Calls() {
		e.trackCall(c)
	}
	e.refresh()
	reply, err := conn.RequestName(Name, dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("%s is already owned by another service", Name)
	}
	go e.loop()
	return e, nil
}

func (e *Exporter) loop() {
	ch, _ := e.backend.Subscribe()
	for ev := range ch {
		switch ev.Kind {
		case phone.EventStatus:
			e.refresh()
		case phone.EventCall:
			e.trackCall(*ev.Call)
		case phone.EventCallGone:
			e.callGone(*ev.Call)
		case phone.EventMessage:
			e.messageEvent(*ev.Message)
		case phone.EventMessageGone:
			e.messageGone(ev.Message.ID)
		}
	}
}

func (e *Exporter) allocate(kind string) dbus.ObjectPath {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := dbus.ObjectPath(fmt.Sprintf("%s/%s/%d", Root, kind, e.next))
	e.next++
	return p
}

func modemState(st phone.Status) int32 {
	switch st.Stage {
	case phone.StageFailed:
		if st.Radio && st.Registration != "" {
			break
		}
		return -1
	case phone.StageStarting, phone.StageModem, phone.StageSIM:
		return 1
	case phone.StageLocked:
		return 2
	case phone.StageOff:
		return 3
	}
	if !st.Radio {
		return 3
	}
	switch st.Registration {
	case "home", "roaming":
		if st.Data {
			return 11
		}
		return 8
	case "searching", "denied", "idle", "unknown", "":
		return 7
	}
	return 6
}

func unlockRequired(st phone.Status) uint32 {
	switch st.SIM.State {
	case "pin":
		return 2
	case "puk":
		return 4
	case "ready":
		return 1
	}
	return 0
}

var techBits = map[string]uint32{"gsm": 1 << 1, "gprs": 1 << 3, "edge": 1 << 4, "umts": 1 << 5, "hsdpa": 1 << 6, "hsupa": 1 << 7, "hspa": 1 << 8, "hspa+": 1 << 9, "lte": 1 << 14, "5gnr": 1 << 15}

func regState(r string) uint32 {
	switch r {
	case "idle":
		return 0
	case "home":
		return 1
	case "searching":
		return 2
	case "denied":
		return 3
	case "roaming":
		return 5
	}
	return 4
}

func (e *Exporter) modemProps(st phone.Status) props {
	return props{
		"Sim":                          dbus.MakeVariant(simPath),
		"SimSlots":                     dbus.MakeVariant([]dbus.ObjectPath{simPath}),
		"PrimarySimSlot":               dbus.MakeVariant(uint32(1)),
		"Bearers":                      dbus.MakeVariant([]dbus.ObjectPath{}),
		"SupportedCapabilities":        dbus.MakeVariant([]uint32{1<<2 | 1<<3}),
		"CurrentCapabilities":          dbus.MakeVariant(uint32(1<<2 | 1<<3)),
		"MaxBearers":                   dbus.MakeVariant(uint32(2)),
		"MaxActiveBearers":             dbus.MakeVariant(uint32(2)),
		"MaxActiveMultiplexedBearers":  dbus.MakeVariant(uint32(0)),
		"Manufacturer":                 dbus.MakeVariant(st.Manufacturer),
		"Model":                        dbus.MakeVariant(st.Model),
		"Revision":                     dbus.MakeVariant(st.Firmware),
		"CarrierConfiguration":         dbus.MakeVariant(""),
		"CarrierConfigurationRevision": dbus.MakeVariant(""),
		"HardwareRevision":             dbus.MakeVariant(""),
		"DeviceIdentifier":             dbus.MakeVariant("sinty-shannon-0"),
		"Device":                       dbus.MakeVariant("/sys/devices/platform/cpif"),
		"Physdev":                      dbus.MakeVariant("/sys/devices/platform/cpif"),
		"Drivers":                      dbus.MakeVariant([]string{"cpif"}),
		"Plugin":                       dbus.MakeVariant("sinty-shannon"),
		"PrimaryPort":                  dbus.MakeVariant("umts_ipc0"),
		"Ports": dbus.MakeVariant([]struct {
			Name string
			Type uint32
		}{{"umts_ipc0", 7}, {"umts_router", 3}, {"rmnet0", 2}}),
		"EquipmentIdentifier": dbus.MakeVariant(""),
		"UnlockRequired":      dbus.MakeVariant(unlockRequired(st)),
		"UnlockRetries":       dbus.MakeVariant(map[uint32]uint32{2: uint32(st.SIM.PINRetries), 4: uint32(st.SIM.PUKRetries)}),
		"State":               dbus.MakeVariant(modemState(st)),
		"StateFailedReason":   dbus.MakeVariant(failedReason(st)),
		"AccessTechnologies":  dbus.MakeVariant(techBits[st.Tech]),
		"SignalQuality": dbus.MakeVariant(struct {
			Q      uint32
			Recent bool
		}{uint32(st.Signal), true}),
		"OwnNumbers":          dbus.MakeVariant([]string{}),
		"PowerState":          dbus.MakeVariant(powerState(st)),
		"SupportedModes":      dbus.MakeVariant([]struct{ A, B uint32 }{{1<<4 | 1<<3 | 1<<2, 1 << 4}}),
		"CurrentModes":        dbus.MakeVariant(struct{ A, B uint32 }{1<<4 | 1<<3 | 1<<2, 1 << 4}),
		"SupportedBands":      dbus.MakeVariant([]uint32{256}),
		"CurrentBands":        dbus.MakeVariant([]uint32{256}),
		"SupportedIpFamilies": dbus.MakeVariant(uint32(7)),
	}
}

func failedReason(st phone.Status) uint32 {
	if st.Stage != phone.StageFailed {
		return 0
	}
	switch st.SIM.State {
	case "absent":
		return 2
	case "error":
		return 3
	}
	return 0
}

func powerState(st phone.Status) uint32 {
	if st.Radio {
		return 3
	}
	return 2
}

func (e *Exporter) addModem() {
	st := e.backend.Status()
	b := e.backend
	e.b.Add(modemPath, map[string]props{
		ifModem:     e.modemProps(st),
		ifModem3gpp: e.props3gpp(st),
		ifVoice:     {"Calls": dbus.MakeVariant([]dbus.ObjectPath{}), "EmergencyOnly": dbus.MakeVariant(!st.Voice)},
		ifMessaging: {"Messages": dbus.MakeVariant([]dbus.ObjectPath{}), "SupportedStorages": dbus.MakeVariant([]uint32{0}), "DefaultStorage": dbus.MakeVariant(uint32(0))},
	}, map[string]map[string]interface{}{
		ifModem: {
			"Enable": func(on bool) *dbus.Error {
				if err := b.SetRadio(on); err != nil {
					return fail("Failed", err)
				}
				return nil
			},
			"ListBearers": func() ([]dbus.ObjectPath, *dbus.Error) { return []dbus.ObjectPath{}, nil },
			"CreateBearer": func(map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
				return "/", fail("Unsupported", errors.New("mobile data is managed by the system"))
			},
			"DeleteBearer": func(dbus.ObjectPath) *dbus.Error {
				return fail("Unsupported", errors.New("mobile data is managed by the system"))
			},
			"Reset":        func() *dbus.Error { return fail("Unsupported", errors.New("reset needs a reboot")) },
			"FactoryReset": func(string) *dbus.Error { return fail("Unsupported", errors.New("not supported")) },
			"SetPowerState": func(s uint32) *dbus.Error {
				if err := b.SetRadio(s == 3); err != nil {
					return fail("Failed", err)
				}
				return nil
			},
			"SetCurrentCapabilities": func(uint32) *dbus.Error { return fail("Unsupported", errors.New("not supported")) },
			"SetCurrentModes":        func(struct{ A, B uint32 }) *dbus.Error { return fail("Unsupported", errors.New("not supported")) },
			"SetCurrentBands":        func([]uint32) *dbus.Error { return fail("Unsupported", errors.New("not supported")) },
			"Command": func(string, uint32) (string, *dbus.Error) {
				return "", fail("Unauthorized", errors.New("raw commands are not exposed"))
			},
		},
		ifModem3gpp: {
			"Register": func(string) *dbus.Error { return nil },
			"Scan": func() ([]map[string]dbus.Variant, *dbus.Error) {
				return nil, fail("Unsupported", errors.New("not supported"))
			},
		},
		ifVoice: {
			"ListCalls":  e.listCalls,
			"CreateCall": e.createCall,
			"DeleteCall": e.deleteCall,
			"HangupAll": func() *dbus.Error {
				for _, c := range b.Calls() {
					b.Hangup(c.ID)
				}
				return nil
			},
		},
		ifMessaging: {
			"List":   e.listMessages,
			"Create": e.createMessage,
			"Delete": e.deleteMessage,
		},
	})
}

func (e *Exporter) props3gpp(st phone.Status) props {
	return props{
		"Imei":                 dbus.MakeVariant(""),
		"RegistrationState":    dbus.MakeVariant(regState(st.Registration)),
		"OperatorCode":         dbus.MakeVariant(st.OperatorCode),
		"OperatorName":         dbus.MakeVariant(st.OperatorName),
		"EnabledFacilityLocks": dbus.MakeVariant(uint32(0)),
		"SubscriptionState":    dbus.MakeVariant(uint32(0)),
		"EpsUeModeOperation":   dbus.MakeVariant(uint32(3)),
		"PacketServiceState":   dbus.MakeVariant(packetState(st)),
	}
}

func packetState(st phone.Status) uint32 {
	if st.Registration == "home" || st.Registration == "roaming" {
		return 2
	}
	return 1
}

func (e *Exporter) addSim() {
	st := e.backend.Status()
	b := e.backend
	e.b.Add(simPath, map[string]props{ifSim: e.simProps(st)}, map[string]map[string]interface{}{
		ifSim: {
			"SendPin": func(pin string) *dbus.Error {
				if _, err := b.SendPIN(pin); err != nil {
					return &dbus.Error{Name: "org.freedesktop.ModemManager1.Error.MobileEquipment.IncorrectPassword", Body: []interface{}{err.Error()}}
				}
				return nil
			},
			"SendPuk": func(string, string) *dbus.Error {
				return fail("Unsupported", errors.New("enter the PUK in another phone"))
			},
			"EnablePin": func(string, bool) *dbus.Error { return fail("Unsupported", errors.New("not supported yet")) },
			"ChangePin": func(string, string) *dbus.Error { return fail("Unsupported", errors.New("not supported yet")) },
		},
	})
}

func (e *Exporter) simProps(st phone.Status) props {
	return props{
		"Active":             dbus.MakeVariant(st.SIM.State != "absent"),
		"SimIdentifier":      dbus.MakeVariant(""),
		"Imsi":               dbus.MakeVariant(""),
		"Eid":                dbus.MakeVariant(""),
		"OperatorIdentifier": dbus.MakeVariant(""),
		"OperatorName":       dbus.MakeVariant(st.SIM.Operator),
		"EmergencyNumbers":   dbus.MakeVariant(st.Emergency),
		"SimType":            dbus.MakeVariant(uint32(1)),
		"EsimStatus":         dbus.MakeVariant(uint32(0)),
		"Removability":       dbus.MakeVariant(uint32(1)),
	}
}

func (e *Exporter) refresh() {
	st := e.backend.Status()
	e.mu.Lock()
	old := e.state
	now := modemState(st)
	e.state = now
	e.mu.Unlock()
	e.b.Update(modemPath, ifModem, e.modemProps(st))
	e.b.Update(modemPath, ifModem3gpp, e.props3gpp(st))
	e.b.Update(modemPath, ifVoice, props{"EmergencyOnly": dbus.MakeVariant(!st.Voice)})
	e.b.Update(simPath, ifSim, e.simProps(st))
	if old != now {
		e.b.Emit(modemPath, ifModem, "StateChanged", old, now, uint32(0))
	}
}

func (e *Exporter) callPaths() []dbus.ObjectPath {
	var out []dbus.ObjectPath
	for p := range e.calls {
		out = append(out, p)
	}
	return out
}

func (e *Exporter) listCalls() ([]dbus.ObjectPath, *dbus.Error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.callPaths(), nil
}

func (e *Exporter) addCallObject(c *callObj, direction int32, reason int32) {
	b := e.backend
	e.b.Add(c.path, map[string]props{ifCall: {
		"State":       dbus.MakeVariant(c.state),
		"StateReason": dbus.MakeVariant(reason),
		"Direction":   dbus.MakeVariant(direction),
		"Number":      dbus.MakeVariant(c.number),
		"Multiparty":  dbus.MakeVariant(false),
		"AudioPort":   dbus.MakeVariant(""),
		"AudioFormat": dbus.MakeVariant(map[string]dbus.Variant{}),
	}}, map[string]map[string]interface{}{ifCall: {
		"Start": func() *dbus.Error {
			e.mu.Lock()
			if c.started {
				e.mu.Unlock()
				return fail("WrongState", errors.New("the call was already started"))
			}
			c.started = true
			e.mu.Unlock()
			call, err := b.Dial(c.number)
			if err != nil {
				e.setCallState(c, callTerminated, reasonError)
				return fail("Failed", err)
			}
			e.mu.Lock()
			delete(e.byCall, c.id)
			c.id = call.ID
			e.byCall[call.ID] = c
			e.mu.Unlock()
			e.setCallState(c, callDialing, reasonOutgoingStarted)
			return nil
		},
		"Accept": func() *dbus.Error {
			if err := b.Accept(c.id); err != nil {
				return fail("Failed", err)
			}
			return nil
		},
		"Hangup": func() *dbus.Error {
			e.mu.Lock()
			id, started := c.id, c.started || !c.created
			e.mu.Unlock()
			if !started {
				e.setCallState(c, callTerminated, reasonTerminated)
				return nil
			}
			if err := b.Hangup(id); err != nil {
				return fail("Failed", err)
			}
			return nil
		},
		"SendDtmf": func(keys string) *dbus.Error {
			if err := b.DTMF(c.id, keys); err != nil {
				return fail("Failed", err)
			}
			return nil
		},
		"Deflect":         func(string) *dbus.Error { return fail("Unsupported", errors.New("not supported")) },
		"JoinMultiparty":  func() *dbus.Error { return fail("Unsupported", errors.New("not supported")) },
		"LeaveMultiparty": func() *dbus.Error { return fail("Unsupported", errors.New("not supported")) },
	}})
	e.mu.Lock()
	paths := e.callPaths()
	e.mu.Unlock()
	e.b.Update(modemPath, ifVoice, props{"Calls": dbus.MakeVariant(paths)})
	e.b.Emit(modemPath, ifVoice, "CallAdded", c.path)
}

func (e *Exporter) createCall(opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	v, ok := opts["number"]
	number, _ := v.Value().(string)
	if !ok || strings.TrimSpace(number) == "" {
		return "/", &dbus.Error{Name: errCore + "InvalidArgs", Body: []interface{}{"a number is required"}}
	}
	c := &callObj{path: e.allocate("Call"), id: "pending-" + string(e.allocate("Pending")), state: callUnknown, created: true, number: number}
	e.mu.Lock()
	e.calls[c.path] = c
	e.byCall[c.id] = c
	e.mu.Unlock()
	e.addCallObject(c, 2, reasonUnknown)
	return c.path, nil
}

func (e *Exporter) deleteCall(path dbus.ObjectPath) *dbus.Error {
	e.mu.Lock()
	c := e.calls[path]
	e.mu.Unlock()
	if c == nil {
		return fail("NotFound", errors.New("no such call"))
	}
	if c.state != callTerminated && c.state != callUnknown {
		e.backend.Hangup(c.id)
	}
	e.dropCall(c)
	return nil
}

func (e *Exporter) dropCall(c *callObj) {
	e.mu.Lock()
	if e.calls[c.path] != c {
		e.mu.Unlock()
		return
	}
	delete(e.calls, c.path)
	delete(e.byCall, c.id)
	paths := e.callPaths()
	e.mu.Unlock()
	e.b.Remove(c.path)
	e.b.Update(modemPath, ifVoice, props{"Calls": dbus.MakeVariant(paths)})
	e.b.Emit(modemPath, ifVoice, "CallDeleted", c.path)
}

func (e *Exporter) setCallState(c *callObj, state, reason int32) {
	e.mu.Lock()
	old := c.state
	c.state = state
	e.mu.Unlock()
	if old == state {
		return
	}
	e.b.Update(c.path, ifCall, props{"State": dbus.MakeVariant(state), "StateReason": dbus.MakeVariant(reason)})
	e.b.Emit(c.path, ifCall, "StateChanged", old, state, uint32(reason))
	if state == callTerminated {
		go func() {
			time.Sleep(30 * time.Second)
			e.dropCall(c)
		}()
	}
}

func (e *Exporter) trackCall(call phone.Call) {
	e.mu.Lock()
	c := e.byCall[call.ID]
	e.mu.Unlock()
	state := callStates[call.State]
	reason := reasonUnknown
	switch call.State {
	case phone.CallActive:
		reason = reasonAccepted
	case phone.CallTerminated:
		reason = reasonTerminated
		if call.Reason == "error" {
			reason = reasonError
		}
	case phone.CallRingingIn:
		reason = reasonIncomingNew
	case phone.CallDialing, phone.CallRingingOut:
		reason = reasonOutgoingStarted
	}
	if c == nil {
		c = &callObj{path: e.allocate("Call"), id: call.ID, state: state, number: call.Number, started: true}
		e.mu.Lock()
		e.calls[c.path] = c
		e.byCall[c.id] = c
		e.mu.Unlock()
		dir := int32(2)
		if call.Incoming {
			dir = 1
		}
		e.addCallObject(c, dir, reason)
		if state == callTerminated {
			e.setCallState(c, callTerminated, reason)
		}
		return
	}
	e.setCallState(c, state, reason)
}

func (e *Exporter) callGone(call phone.Call) {
	e.mu.Lock()
	c := e.byCall[call.ID]
	e.mu.Unlock()
	if c != nil {
		reason := reasonTerminated
		if call.Reason == "error" {
			reason = reasonError
		}
		e.setCallState(c, callTerminated, reason)
	}
}

func smsState(m phone.Message) uint32 {
	switch m.State {
	case "received":
		return 3
	case "sending":
		return 4
	case "sent":
		return 5
	}
	return 1
}

func (e *Exporter) smsPaths() []dbus.ObjectPath {
	var out []dbus.ObjectPath
	for p := range e.sms {
		out = append(out, p)
	}
	return out
}

func (e *Exporter) addMessage(m phone.Message) *smsObj {
	e.mu.Lock()
	if o := e.bySMS[m.ID]; o != nil {
		e.mu.Unlock()
		e.b.Update(o.path, ifSms, props{"State": dbus.MakeVariant(smsState(m))})
		return o
	}
	e.mu.Unlock()
	o := &smsObj{path: e.allocate("SMS"), id: m.ID, number: m.Number, text: m.Text}
	e.addSMSObject(o, m)
	return o
}

func (e *Exporter) addSMSObject(o *smsObj, m phone.Message) {
	pdu := uint32(2)
	if m.Incoming {
		pdu = 1
	}
	data := m.Data
	if data == nil {
		data = []byte{}
	}
	e.mu.Lock()
	e.sms[o.path] = o
	if o.id != "" {
		e.bySMS[o.id] = o
	}
	paths := e.smsPaths()
	e.mu.Unlock()
	b := e.backend
	e.b.Add(o.path, map[string]props{ifSms: {
		"State":   dbus.MakeVariant(smsState(m)),
		"PduType": dbus.MakeVariant(pdu),
		"Number":  dbus.MakeVariant(m.Number),
		"Text":    dbus.MakeVariant(m.Text),
		"Data":    dbus.MakeVariant(data),
		"SMSC":    dbus.MakeVariant(""),
		"Validity": dbus.MakeVariant(struct {
			T uint32
			V dbus.Variant
		}{0, dbus.MakeVariant(uint32(0))}),
		"Class":                 dbus.MakeVariant(int32(m.Class)),
		"TeleserviceId":         dbus.MakeVariant(uint32(0)),
		"ServiceCategory":       dbus.MakeVariant(uint32(0)),
		"DeliveryReportRequest": dbus.MakeVariant(false),
		"MessageReference":      dbus.MakeVariant(uint32(0)),
		"Timestamp":             dbus.MakeVariant(m.Timestamp.Format(time.RFC3339)),
		"DischargeTimestamp":    dbus.MakeVariant(""),
		"DeliveryState":         dbus.MakeVariant(uint32(0x100)),
		"Storage":               dbus.MakeVariant(uint32(0)),
	}}, map[string]map[string]interface{}{ifSms: {
		"Send": func() *dbus.Error {
			e.mu.Lock()
			draft, number, text := o.draft, o.number, o.text
			o.draft = false
			e.mu.Unlock()
			if !draft {
				return fail("WrongState", errors.New("this message was already sent"))
			}
			e.b.Update(o.path, ifSms, props{"State": dbus.MakeVariant(uint32(4))})
			sent, err := b.SendMessage(number, text)
			if sent.ID != "" {
				e.mu.Lock()
				o.id = sent.ID
				e.bySMS[sent.ID] = o
				e.mu.Unlock()
			}
			if err != nil {
				e.b.Update(o.path, ifSms, props{"State": dbus.MakeVariant(smsState(sent))})
				return fail("Failed", err)
			}
			e.b.Update(o.path, ifSms, props{"State": dbus.MakeVariant(uint32(5)), "Timestamp": dbus.MakeVariant(sent.Timestamp.Format(time.RFC3339))})
			return nil
		},
		"Store": func(uint32) *dbus.Error { return nil },
	}})
	e.b.Update(modemPath, ifMessaging, props{"Messages": dbus.MakeVariant(paths)})
	e.b.Emit(modemPath, ifMessaging, "Added", o.path, m.Incoming)
}

func (e *Exporter) messageEvent(m phone.Message) {
	e.mu.Lock()
	o := e.bySMS[m.ID]
	e.mu.Unlock()
	if o != nil {
		e.b.Update(o.path, ifSms, props{"State": dbus.MakeVariant(smsState(m))})
		return
	}
	e.addMessage(m)
}

func (e *Exporter) messageGone(id string) {
	e.mu.Lock()
	o := e.bySMS[id]
	e.mu.Unlock()
	if o != nil {
		e.dropSMS(o)
	}
}

func (e *Exporter) dropSMS(o *smsObj) {
	e.mu.Lock()
	if e.sms[o.path] != o {
		e.mu.Unlock()
		return
	}
	delete(e.sms, o.path)
	delete(e.bySMS, o.id)
	paths := e.smsPaths()
	e.mu.Unlock()
	e.b.Remove(o.path)
	e.b.Update(modemPath, ifMessaging, props{"Messages": dbus.MakeVariant(paths)})
	e.b.Emit(modemPath, ifMessaging, "Deleted", o.path)
}

func (e *Exporter) listMessages() ([]dbus.ObjectPath, *dbus.Error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.smsPaths(), nil
}

func (e *Exporter) createMessage(opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	number, _ := opts["number"].Value().(string)
	text, _ := opts["text"].Value().(string)
	if strings.TrimSpace(number) == "" || text == "" {
		return "/", &dbus.Error{Name: errCore + "InvalidArgs", Body: []interface{}{"a number and a text are required"}}
	}
	o := &smsObj{path: e.allocate("SMS"), number: number, text: text, draft: true}
	e.addSMSObject(o, phone.Message{Number: number, Text: text, Timestamp: time.Now(), State: "stored", Class: -1})
	return o.path, nil
}

func (e *Exporter) deleteMessage(path dbus.ObjectPath) *dbus.Error {
	e.mu.Lock()
	o := e.sms[path]
	e.mu.Unlock()
	if o == nil {
		return fail("NotFound", errors.New("no such message"))
	}
	if o.id != "" {
		if err := e.backend.DeleteMessage(o.id); err != nil {
			log.Printf("mm: delete message: %v", err)
		}
	}
	e.dropSMS(o)
	return nil
}
