package shannon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/apn"
	"github.com/singularityos-lab/sinty-modem/internal/phone"
	"github.com/singularityos-lab/sinty-modem/internal/shannon/cp"
	"github.com/singularityos-lab/sinty-modem/internal/shannon/ril"
	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
	"github.com/singularityos-lab/sinty-modem/internal/sms"
	"github.com/singularityos-lab/sinty-modem/internal/store"
)

type Settings struct {
	APN         string
	IMSAPN      string
	IPType      string
	AllowNR     bool
	APNDatabase string
	Identity    ril.Identity
	SIMTimeout  time.Duration
	RegTimeout  time.Duration
	IMSTimeout  time.Duration
	RetryDelay  time.Duration
	SlotFile    string
}

type Audio interface {
	Start() error
	Stop() error
	Mute(on bool) error
}

type Network interface {
	Up(iface string, b ril.Bearer) error
	Down(iface string) error
}

type call struct {
	phone.Call
	modemID int
}

type Service struct {
	ril      *ril.RIL
	store    *store.Store
	settings Settings
	audio    Audio
	net      Network

	mu       sync.Mutex
	status   phone.Status
	calls    map[string]*call
	pending  string
	muted    bool
	pinWait  chan struct{}
	simWake  chan struct{}
	restart  chan struct{}
	radioOff bool
	subs     map[chan phone.Event]struct{}
	carrier  *apn.Carrier
	internet ril.Bearer
	imsReg   ril.IMSRegistration
	rsrp     int
	hasRSRP  bool
}

func New(r *ril.RIL, st *store.Store, s Settings, audio Audio, network Network) *Service {
	if s.SIMTimeout == 0 {
		s.SIMTimeout = 120 * time.Second
	}
	if s.RegTimeout == 0 {
		s.RegTimeout = 180 * time.Second
	}
	if s.IMSTimeout == 0 {
		s.IMSTimeout = 120 * time.Second
	}
	if s.RetryDelay == 0 {
		s.RetryDelay = 30 * time.Second
	}
	if s.IPType == "" {
		s.IPType = "ipv4v6"
	}
	if s.APNDatabase == "" {
		s.APNDatabase = apn.Database
	}
	return &Service{
		ril:      r,
		store:    st,
		settings: s,
		audio:    audio,
		net:      network,
		calls:    map[string]*call{},
		subs:     map[chan phone.Event]struct{}{},
		pinWait:  make(chan struct{}, 1),
		simWake:  make(chan struct{}, 1),
		restart:  make(chan struct{}, 1),
		status: phone.Status{
			Stage:        phone.StageStarting,
			Manufacturer: s.Identity.Manufacturer,
			Model:        s.Identity.Model,
			SIM:          phone.SIM{State: string(ril.SIMUnknown)},
			IMS:          "unknown",
			Emergency:    []string{"112", "911"},
		},
	}
}

func (s *Service) SetFirmware(v string) {
	s.mu.Lock()
	s.status.Firmware = v
	s.mu.Unlock()
}

func (s *Service) emit(ev phone.Event) {
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *Service) update(f func(*phone.Status)) {
	s.mu.Lock()
	before := s.status
	f(&s.status)
	changed := fmt.Sprint(before) != fmt.Sprint(s.status)
	if changed {
		s.emit(phone.Event{Kind: phone.EventStatus})
	}
	s.mu.Unlock()
}

func (s *Service) stage(st phone.Stage, err error) {
	s.update(func(x *phone.Status) {
		x.Stage = st
		x.Error = ""
		if err != nil {
			x.Error = err.Error()
		}
	})
	if err != nil {
		log.Printf("modem: %s: %v", st, err)
	} else {
		log.Printf("modem: %s", st)
	}
}

func (s *Service) Subscribe() (<-chan phone.Event, func()) {
	ch := make(chan phone.Event, 64)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

func (s *Service) Status() phone.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	st.Emergency = append([]string(nil), s.status.Emergency...)
	return st
}

func (s *Service) Calls() []phone.Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []phone.Call
	for _, c := range s.calls {
		out = append(out, c.Call)
	}
	return out
}

func (s *Service) Messages() []phone.Message { return s.store.List() }

func (s *Service) Run(ctx context.Context) error {
	s.stage(phone.StageModem, nil)
	s.recoverStoredSMS()
	go s.events(ctx)
	go s.poll(ctx)
	for {
		err := s.bringup(ctx)
		if err == nil || ctx.Err() != nil {
			break
		}
		s.stage(phone.StageFailed, err)
		if !s.awaitRetry(ctx) {
			break
		}
		s.remap(nil)
	}
	<-ctx.Done()
	s.mu.Lock()
	var live []*call
	for _, c := range s.calls {
		if c.State != phone.CallTerminated && c.modemID >= 0 {
			live = append(live, c)
		}
	}
	s.mu.Unlock()
	for _, c := range live {
		s.ril.Hangup(byte(c.modemID))
	}
	if s.audio != nil {
		s.audio.Stop()
	}
	return nil
}

func (s *Service) wait(ctx context.Context, timeout time.Duration, check func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := check()
		if err != nil && !errors.Is(err, errRetry) {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return context.DeadlineExceeded
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

var (
	errRetry  = errors.New("retry")
	errInCall = errors.New("a call is in progress")
)

func (s *Service) wake() {
	select {
	case s.simWake <- struct{}{}:
	default:
	}
}

func (s *Service) awaitRetry(ctx context.Context) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-s.simWake:
		case <-time.After(s.settings.RetryDelay):
		}
		s.mu.Lock()
		off := s.radioOff
		s.mu.Unlock()
		if !off {
			return true
		}
	}
}

func SavedSlot(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	slot, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return slot, err == nil
}

func saveSlot(path string, slot int) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path+".new", []byte(strconv.Itoa(slot)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".new", path)
}

func (s *Service) remap(table []byte) {
	logf := func(f string, a ...any) { log.Printf("modem: "+f, a...) }
	if slot, ok := SavedSlot(s.settings.SlotFile); ok {
		if table != nil {
			return
		}
		if err := ril.SelectSlot(s.ril.Client(), slot, 8*time.Second); err != nil {
			logf("saved slot %d: %v", slot, err)
		}
		return
	}
	mapped, err := s.ril.RemapEUICC(table, logf)
	if err != nil {
		logf("slot mapping: %v", err)
	} else if mapped {
		s.wake()
	}
}

func (s *Service) bringup(ctx context.Context) error {
	if s.Status().Stage != phone.StageFailed {
		s.stage(phone.StageSIM, nil)
	}
	var last ril.SIMStatus
	err := s.wait(ctx, s.settings.SIMTimeout, func() (bool, error) {
		st, err := s.ril.SIMStatus()
		if err != nil {
			return false, errRetry
		}
		last = st
		s.applySIM(st)
		return st.USIMReady(), nil
	})
	for err == context.DeadlineExceeded && (last.State() == ril.SIMPIN || last.State() == ril.SIMPUK) {
		s.stage(phone.StageLocked, nil)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.pinWait:
		}
		s.stage(phone.StageSIM, nil)
		err = s.wait(ctx, s.settings.SIMTimeout, func() (bool, error) {
			st, err := s.ril.SIMStatus()
			if err != nil {
				return false, errRetry
			}
			last = st
			s.applySIM(st)
			return st.USIMReady(), nil
		})
	}
	if err != nil {
		if err == context.DeadlineExceeded {
			return fmt.Errorf("no ready SIM (%s)", last.State())
		}
		return err
	}
	s.chooseCarrier(last)
	internet, imsAPN := s.settings.APN, s.settings.IMSAPN
	if internet == "" && s.carrier != nil {
		internet = s.carrier.Internet
	}
	if imsAPN == "" && s.carrier != nil && s.carrier.IMS != "" {
		imsAPN = s.carrier.IMS
	}
	if imsAPN == "" {
		imsAPN = "ims"
	}
	ipType := s.settings.IPType

	s.stage(phone.StageAttach, nil)
	level, lerr := s.ril.RadioLevel()
	alreadyOn := lerr == nil && level == 1
	if lerr != nil {
		log.Printf("modem: radio state unreadable (%v), switching it off before the attach settings", lerr)
		s.ril.SetRadio(false)
	}
	attach := internet
	if s.carrier != nil && s.carrier.Attach != "" && s.settings.APN == "" {
		attach = s.carrier.Attach
	}
	if attach != "" {
		if err := s.ril.InitialAttach(attach, ipType, false); err != nil {
			return fmt.Errorf("initial attach APN: %w", err)
		}
	}
	if internet != "" {
		if err := s.ril.SetProfile(ril.Profile{ID: 0, APN: internet, Types: ril.APNTypes["default"], IP: ipType}); err != nil {
			return fmt.Errorf("data profile: %w", err)
		}
	}
	nrAfter := false
	if !s.settings.AllowNR {
		if err := s.ril.SetNRMode(0); err != nil {
			if alreadyOn {
				log.Printf("modem: NR mode: %v", err)
			} else {
				nrAfter = true
			}
		}
	}
	if !alreadyOn {
		if err := s.ril.SetRadio(true); err != nil {
			return fmt.Errorf("radio on: %w", err)
		}
		if nrAfter {
			if err := s.ril.SetNRMode(0); err != nil {
				log.Printf("modem: NR mode after radio on: %v", err)
			}
		}
	}
	s.update(func(x *phone.Status) { x.Radio = true })

	s.stage(phone.StageRegistering, nil)
	began := time.Now()
	err = s.wait(ctx, s.settings.RegTimeout, func() (bool, error) {
		reg, err := s.ril.PSRegistration()
		if err != nil {
			return false, errRetry
		}
		s.applyRegistration(reg)
		return reg.Registered() && (s.settings.AllowNR || reg.RAT == 14 || reg.RAT == 20), nil
	})
	if err != nil {
		if err == context.DeadlineExceeded {
			return errors.New("no packet registration on the network")
		}
		return err
	}
	log.Printf("modem: packet registered after %s", time.Since(began).Round(100*time.Millisecond))
	s.refreshOperator()

	var attachBearer ril.Bearer
	if list, err := s.ril.Bearers(); err == nil {
		for _, b := range list {
			log.Printf("modem: listed at registration: %s", b)
			if b.IMSCapable() {
				attachBearer = b
			}
		}
	}

	if internet != "" {
		s.stage(phone.StageData, nil)
		if err := s.startInternet(internet, ipType); err != nil {
			log.Printf("modem: mobile data unavailable: %v", err)
		}
	}

	s.stage(phone.StageIMS, nil)
	s.update(func(x *phone.Status) { x.IMS = "starting" })
	if err := s.ril.StartIMSStack(); err != nil {
		return s.imsFailed(fmt.Errorf("IMS stack start: %w", err))
	}
	cid := ril.IMSCID
	bearer := attachBearer
	if bearer != nil {
		cid = bearer.CID()
		log.Printf("modem: IMS on the modem's attach bearer CID %d", cid)
	} else {
		b, err := s.ril.Activate(ril.IMSCID, imsAPN, "ipv6", true, 0)
		if err != nil {
			b, err = s.ril.Activate(ril.IMSCID, imsAPN, "ipv4v6", true, 0)
		}
		if err != nil {
			return s.imsFailed(fmt.Errorf("IMS bearer: %w", err))
		}
		bearer = b
		if v4, v6 := b.PCSCF(); len(v4)+len(v6) == 0 {
			if list, err := s.ril.Bearers(); err == nil {
				for _, x := range list {
					if x.CID() == ril.IMSCID && x.IMSCapable() {
						bearer = x
					}
				}
			}
		}
	}
	if err := s.ril.RegisterIMS(bearer, cid, s.settings.Identity, nil); err != nil {
		return s.imsFailed(err)
	}
	err = s.wait(ctx, s.settings.IMSTimeout, func() (bool, error) {
		s.mu.Lock()
		g := s.imsReg
		s.mu.Unlock()
		return g.Voice() && g.SMS(), nil
	})
	if err != nil {
		if err == context.DeadlineExceeded {
			return s.imsFailed(errors.New("the network did not register IMS voice and SMS"))
		}
		return err
	}
	s.update(func(x *phone.Status) { x.IMS = "registered" })
	s.stage(phone.StageReady, nil)
	return nil
}

func (s *Service) imsFailed(err error) error {
	s.update(func(x *phone.Status) { x.IMS = "failed" })
	log.Printf("modem: IMS: %v", err)
	s.stage(phone.StageReady, err)
	return nil
}

func (s *Service) startInternet(internet, ipType string) error {
	b, err := s.ril.Activate(ril.InternetCID, internet, ipType, false, 0)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.internet = b
	s.mu.Unlock()
	if s.net != nil {
		if err := s.net.Up(fmt.Sprintf("rmnet%d", ril.InternetCID-1), b); err != nil {
			return err
		}
	}
	s.update(func(x *phone.Status) { x.Data = true })
	return nil
}

func (s *Service) chooseCarrier(st ril.SIMStatus) {
	app := st.App(ril.AppUSIM)
	if app == nil {
		return
	}
	imsi, err := s.ril.IMSI(app.AID)
	if err != nil {
		log.Printf("modem: the SIM's home network could not be read: %v", err)
		return
	}
	mcc, mncs := ril.HomePLMN(imsi)
	car, err := apn.Lookup(s.settings.APNDatabase, mcc, mncs)
	if err != nil {
		log.Printf("modem: carrier database: %v", err)
		return
	}
	if car == nil {
		log.Printf("modem: no carrier for this SIM in the database; calls and SMS without mobile data")
		return
	}
	s.carrier = car
	s.update(func(x *phone.Status) { x.SIM.Operator = car.Name })
	log.Printf("modem: carrier settings from the database (%s)", car.Name)
}

func (s *Service) applySIM(st ril.SIMStatus) {
	s.update(func(x *phone.Status) {
		x.SIM.State = string(st.State())
		if app := st.Main(); app != nil {
			x.SIM.PINRetries = app.PINRetries
			x.SIM.PUKRetries = app.PUKRetries
		}
		x.Emergency = []string{"112", "911"}
		if st.State() == ril.SIMAbsent {
			x.Emergency = append(x.Emergency, "000", "08", "110", "118", "119", "999")
		}
	})
}

var rats = map[byte]string{1: "gprs", 2: "edge", 3: "umts", 9: "hsdpa", 10: "hsupa", 11: "hspa", 14: "lte", 15: "hspa+", 16: "gsm", 20: "lte", 21: "5gnr", 22: "5gnr"}

func (s *Service) applyRegistration(reg ril.Registration) {
	s.update(func(x *phone.Status) {
		switch reg.State {
		case 1:
			x.Registration = "home"
		case 5:
			x.Registration = "roaming"
		case 2:
			x.Registration = "searching"
		case 3:
			x.Registration = "denied"
		case 0:
			x.Registration = "idle"
		default:
			x.Registration = "unknown"
		}
		x.Tech = rats[reg.RAT]
	})
}

func (s *Service) refreshOperator() {
	o, err := s.ril.Operator()
	if err != nil {
		return
	}
	s.update(func(x *phone.Status) {
		x.OperatorCode = o.PLMN
		x.OperatorName = o.Name
	})
}

func (s *Service) poll(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if st, err := s.ril.SIMStatus(); err == nil {
			s.applySIM(st)
		}
		if on, err := s.ril.RadioOn(); err == nil {
			s.update(func(x *phone.Status) { x.Radio = on })
		}
		if reg, err := s.ril.PSRegistration(); err == nil {
			s.applyRegistration(reg)
			if reg.Registered() {
				s.refreshOperator()
			}
		}
		if st, err := cp.Status(); err == nil && st != cp.Online {
			s.stage(phone.StageFailed, fmt.Errorf("the modem left ONLINE (%s)", st))
		}
	}
}

func (s *Service) events(ctx context.Context) {
	c := s.ril.Client()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.Done():
			s.stage(phone.StageFailed, errors.New("the modem channel closed"))
			return
		case ind, ok := <-c.Indications():
			if !ok {
				return
			}
			s.handle(ind)
		}
	}
}

func (s *Service) handle(ind sit.Indication) {
	switch ind.ID {
	case sit.IndNewSMS, sit.IndNewSMSReport, sit.IndIMSNewSMS, sit.IndIMSSMSReport:
		s.incomingSMS(ind)
	case sit.IndIMSRegistration:
		g, err := ril.ParseIMSRegistration(ind.Raw)
		if err != nil {
			log.Printf("modem: %v", err)
			return
		}
		log.Printf("modem: IMS registration state %d features %d status %d %s", g.State, g.Features, g.Status, g.Reason)
		s.mu.Lock()
		s.imsReg = g
		s.mu.Unlock()
		s.update(func(x *phone.Status) {
			x.Voice = g.Voice()
			x.SMS = g.SMS()
			if g.Voice() || g.SMS() {
				x.IMS = "registered"
			} else if x.IMS == "registered" {
				x.IMS = "lost"
			}
		})
	case sit.IndRadioState:
		if on, ok := ril.RadioIndication(ind.Payload); ok {
			s.update(func(x *phone.Status) { x.Radio = on })
		}
	case sit.IndPhoneReset:
		log.Printf("modem: reset indication (%d bytes)", len(ind.Payload))
	case sit.IndSignal:
		v, ok := ril.LTERSRP(ind.Payload)
		s.update(func(x *phone.Status) { x.Signal = ril.SignalPercent(v, ok) })
	case sit.IndSlotStatus:
		go s.remap(ind.Payload)
	case sit.IndNetworkState, sit.IndSIMStatus:
		go func() {
			if st, err := s.ril.SIMStatus(); err == nil {
				s.applySIM(st)
				if ind.ID == sit.IndSIMStatus && st.USIMReady() {
					s.wake()
				}
			}
			if reg, err := s.ril.PSRegistration(); err == nil {
				s.applyRegistration(reg)
			}
		}()
	case sit.IndRegFailed:
		if f, err := ril.ParseRegFailure(ind.Raw); err == nil {
			log.Printf("modem: registration failed: domain %d cause %d additional %d", f.Domain, f.Cause, f.Additional)
		}
	case sit.IndDataCallList:
		s.dataChanged(ind.Payload)
	case sit.IndCallRing:
		r, err := ril.ParseRing(ind.Raw)
		if err != nil || r.Type != 1 {
			return
		}
		s.mu.Lock()
		for _, c := range s.calls {
			if c.modemID == int(r.ID) && c.State != phone.CallTerminated {
				s.mu.Unlock()
				return
			}
		}
		c := &call{Call: phone.Call{ID: store.NewID(), Number: r.Caller, Incoming: true, State: phone.CallRingingIn, Since: time.Now()}, modemID: int(r.ID)}
		s.calls[c.ID] = c
		cc := c.Call
		s.emit(phone.Event{Kind: phone.EventCall, Call: &cc})
		s.mu.Unlock()
		log.Printf("modem: incoming call")
	case sit.IndCallStatus:
		cs, err := ril.ParseCallStatus(ind.Raw)
		if err != nil {
			log.Printf("modem: %v", err)
			return
		}
		if cs.Type != 1 {
			return
		}
		s.callStatus(cs)
	case sit.IndCarrierSIMInfo:
		log.Printf("modem: the modem asked for a carrier configuration")
	case sit.IndCarrierStatus:
		log.Printf("modem: carrier configuration status (%d bytes)", len(ind.Payload))
	case sit.IndPhysicalChannel:
	default:
		log.Printf("modem: indication %s (%d bytes)", sit.Name(ind.ID), len(ind.Payload))
	}
}

var callStates = map[byte]phone.CallState{
	ril.CallActive: phone.CallActive, ril.CallHeld: phone.CallHeld, ril.CallDialing: phone.CallDialing,
	ril.CallAlerting: phone.CallRingingOut, ril.CallIncoming: phone.CallRingingIn, ril.CallWaiting: phone.CallWaiting,
	ril.CallTerminated: phone.CallTerminated, ril.CallHeld2: phone.CallHeld, ril.CallActive2: phone.CallActive,
}

func (s *Service) callStatus(cs ril.CallStatus) {
	s.mu.Lock()
	var c *call
	for _, x := range s.calls {
		if x.modemID == int(cs.ID) && x.State != phone.CallTerminated {
			c = x
		}
	}
	if c == nil && s.pending != "" {
		c = s.calls[s.pending]
		if c != nil {
			c.modemID = int(cs.ID)
		}
		s.pending = ""
	}
	if c == nil {
		s.mu.Unlock()
		return
	}
	c.State = callStates[cs.State]
	if cs.State == ril.CallTerminated {
		c.Reason = "terminated"
		if cs.Cause != ril.CauseNormal {
			c.Reason = "error"
		}
	}
	cc := c.Call
	s.emit(phone.Event{Kind: phone.EventCall, Call: &cc})
	live := 0
	for _, x := range s.calls {
		if x.State != phone.CallTerminated {
			live++
		}
	}
	if cs.State == ril.CallTerminated {
		delete(s.calls, c.ID)
		s.emit(phone.Event{Kind: phone.EventCallGone, Call: &cc})
	}
	s.mu.Unlock()
	if live == 0 && s.audio != nil {
		if err := s.audio.Stop(); err != nil {
			log.Printf("modem: call audio cleanup: %v", err)
		}
	}
}

func (s *Service) dataChanged(payload []byte) {
	list, err := ril.ParseBearers(payload)
	if err != nil {
		log.Printf("modem: data-call list changed: %v", err)
		return
	}
	s.mu.Lock()
	internet := s.internet
	s.mu.Unlock()
	if internet == nil {
		return
	}
	for _, b := range list {
		if b.CID() == internet.CID() {
			return
		}
	}
	log.Printf("modem: the internet bearer was dropped by the network")
	s.mu.Lock()
	s.internet = nil
	s.mu.Unlock()
	if s.net != nil {
		s.net.Down(fmt.Sprintf("rmnet%d", ril.InternetCID-1))
	}
	s.update(func(x *phone.Status) { x.Data = false })
	go s.reconnect()
}

func (s *Service) reconnect() {
	delay := 3 * time.Second
	for i := 0; i < 40; i++ {
		time.Sleep(delay)
		st := s.Status()
		if st.Stage == phone.StageOff || !st.Radio {
			return
		}
		internet := s.settings.APN
		if internet == "" && s.carrier != nil {
			internet = s.carrier.Internet
		}
		if internet == "" {
			return
		}
		if err := s.startInternet(internet, s.settings.IPType); err == nil {
			log.Printf("modem: internet bearer set up again")
			return
		} else {
			log.Printf("modem: internet bearer again: %v", err)
		}
		if delay < 5*time.Minute {
			delay *= 2
		}
	}
}

func (s *Service) recoverStoredSMS() {
	for id, raw := range s.store.Raw() {
		s.ingest(id, raw)
	}
}

func (s *Service) incomingSMS(ind sit.Indication) {
	if _, err := ril.SMSPDU(ind.Raw); err != nil {
		log.Printf("modem: %v", err)
		return
	}
	id, err := s.store.KeepRaw(ind.Raw)
	if err != nil {
		log.Printf("modem: SMS not stored, not acknowledged: %v", err)
		return
	}
	if err := s.ril.AckSMS(ind.ID, ind.Raw[8]); err != nil {
		log.Printf("modem: SMS acknowledgement: %v", err)
	}
	if ind.ID == sit.IndNewSMSReport || ind.ID == sit.IndIMSSMSReport {
		s.store.DropRaw(id)
		return
	}
	s.ingest(id, ind.Raw)
}

func (s *Service) ingest(id string, raw []byte) {
	pdu, err := ril.SMSPDU(raw)
	if err != nil {
		s.store.DropRaw(id)
		return
	}
	m, err := sms.Received(pdu)
	if err != nil {
		log.Printf("modem: stored SMS could not be decoded: %v", err)
		return
	}
	text, data := m.Text, m.Data
	when := m.Timestamp
	if m.Concat != nil && m.Concat.Total > 1 {
		parts, err := s.store.AddPart(store.Part{Sender: m.Number, Ref: m.Concat.Ref, Total: m.Concat.Total, Seq: m.Concat.Seq, Text: m.Text, Data: m.Data, Time: m.Timestamp, Got: time.Now()})
		if err != nil {
			log.Printf("modem: SMS part: %v", err)
			return
		}
		s.store.DropRaw(id)
		if parts == nil {
			return
		}
		text, data = "", nil
		for _, p := range parts {
			text += p.Text
			data = append(data, p.Data...)
		}
		when = parts[0].Time
	}
	msg := phone.Message{ID: id, Number: m.Number, Text: text, Data: data, Timestamp: when, Incoming: true, State: "received", Class: m.Class}
	if err := s.store.Put(msg); err != nil {
		log.Printf("modem: SMS: %v", err)
		return
	}
	s.store.DropRaw(id)
	s.mu.Lock()
	s.emit(phone.Event{Kind: phone.EventMessage, Message: &msg})
	s.mu.Unlock()
}

var voiceNumber = regexp.MustCompile(`^\+?[0-9*#]{1,40}$`)

func (s *Service) Dial(number string) (phone.Call, error) {
	n, err := sms.Number(number)
	if err != nil && !voiceNumber.MatchString(number) {
		return phone.Call{}, err
	}
	if err != nil {
		n = number
	}
	st := s.Status()
	emergency := false
	for _, e := range st.Emergency {
		if n == e {
			emergency = true
		}
	}
	if !st.Voice && !emergency {
		return phone.Call{}, phone.ErrNotReady
	}
	s.mu.Lock()
	for _, c := range s.calls {
		if c.State != phone.CallTerminated {
			s.mu.Unlock()
			return phone.Call{}, phone.ErrBusy
		}
	}
	c := &call{Call: phone.Call{ID: store.NewID(), Number: n, State: phone.CallDialing, Since: time.Now()}, modemID: -1}
	s.calls[c.ID] = c
	s.pending = c.ID
	cc := c.Call
	s.emit(phone.Event{Kind: phone.EventCall, Call: &cc})
	s.mu.Unlock()
	if s.audio != nil {
		if err := s.audio.Start(); err != nil {
			log.Printf("modem: call audio refused, the call goes on without it: %v", err)
		}
	}
	if err := s.ril.Dial(n); err != nil {
		s.mu.Lock()
		c.State = phone.CallTerminated
		c.Reason = "error"
		s.pending = ""
		cc := c.Call
		delete(s.calls, c.ID)
		s.emit(phone.Event{Kind: phone.EventCall, Call: &cc})
		s.emit(phone.Event{Kind: phone.EventCallGone, Call: &cc})
		s.mu.Unlock()
		if s.audio != nil {
			s.audio.Stop()
		}
		return phone.Call{}, fmt.Errorf("the network rejected the call: %w", err)
	}
	return cc, nil
}

func (s *Service) find(id string) (*call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.calls[id]
	if c == nil {
		return nil, phone.ErrNoCall
	}
	return c, nil
}

func (s *Service) Accept(id string) error {
	c, err := s.find(id)
	if err != nil {
		return err
	}
	if c.State != phone.CallRingingIn && c.State != phone.CallWaiting {
		return errors.New("the call is not ringing")
	}
	if s.audio != nil {
		if err := s.audio.Start(); err != nil {
			log.Printf("modem: call audio refused, the call goes on without it: %v", err)
		}
	}
	return s.ril.Answer()
}

func (s *Service) Hangup(id string) error {
	c, err := s.find(id)
	if err != nil {
		return err
	}
	if c.modemID < 0 {
		return errors.New("the call setup is still pending")
	}
	return s.ril.Hangup(byte(c.modemID))
}

var dtmfKeys = regexp.MustCompile(`^[0-9*#A-Da-d]{1,32}$`)

func (s *Service) DTMF(id, keys string) error {
	c, err := s.find(id)
	if err != nil {
		return err
	}
	if c.State != phone.CallActive || c.modemID < 0 {
		return errors.New("touch tones need an active call")
	}
	if !dtmfKeys.MatchString(keys) {
		return errors.New("invalid touch tones")
	}
	for i := 0; i < len(keys); i++ {
		if err := s.ril.DTMF(byte(c.modemID), keys[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) SetMute(on bool) error {
	if s.audio == nil {
		return errors.New("no call audio")
	}
	return s.audio.Mute(on)
}

func (s *Service) SendMessage(number, text string) (phone.Message, error) {
	pdus, err := sms.Submit(number, text)
	if err != nil {
		return phone.Message{}, err
	}
	dest, _ := sms.Number(number)
	if !s.Status().SMS {
		return phone.Message{}, phone.ErrNotReady
	}
	smsc, err := s.ril.SMSC()
	if err != nil {
		return phone.Message{}, err
	}
	msg := phone.Message{ID: store.NewID(), Number: dest, Text: text, Timestamp: time.Now(), State: "sending", Class: -1}
	if err := s.store.Put(msg); err != nil {
		return msg, err
	}
	sent := 0
	for _, pdu := range pdus {
		if err := s.ril.SendSMS(smsc, pdu); err != nil {
			msg.State = "failed"
			if sent > 0 {
				msg.State = "unknown"
			}
			s.store.Put(msg)
			return msg, err
		}
		sent++
	}
	msg.State = "sent"
	s.store.Put(msg)
	s.mu.Lock()
	s.emit(phone.Event{Kind: phone.EventMessage, Message: &msg})
	s.mu.Unlock()
	return msg, nil
}

func (s *Service) DeleteMessage(id string) error {
	if err := s.store.Delete(id); err != nil {
		return err
	}
	s.mu.Lock()
	s.emit(phone.Event{Kind: phone.EventMessageGone, Message: &phone.Message{ID: id}})
	s.mu.Unlock()
	return nil
}

func (s *Service) SendPIN(pin string) (int, error) {
	left, err := s.ril.VerifyPIN(pin)
	if st, serr := s.ril.SIMStatus(); serr == nil {
		s.applySIM(st)
	}
	if err == nil {
		select {
		case s.pinWait <- struct{}{}:
		default:
		}
	}
	return left, err
}

func (s *Service) idle() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c.State != phone.CallTerminated {
			return errInCall
		}
	}
	return nil
}

func (s *Service) SelectSlot(slot int) error {
	if err := s.idle(); err != nil {
		return err
	}
	if err := ril.SelectSlot(s.ril.Client(), slot, 8*time.Second); err != nil {
		return fmt.Errorf("select slot %d: %w", slot, err)
	}
	log.Printf("modem: physical slot %d put on the first line", slot)
	if err := saveSlot(s.settings.SlotFile, slot); err != nil {
		log.Printf("modem: the slot choice was not saved: %v", err)
	}
	s.mu.Lock()
	s.radioOff = false
	s.mu.Unlock()
	s.wake()
	s.reload()
	return nil
}

func (s *Service) Restart() error {
	if err := s.idle(); err != nil {
		return err
	}
	log.Printf("modem: service restart requested")
	select {
	case s.restart <- struct{}{}:
	default:
	}
	return nil
}

func (s *Service) Restarting() <-chan struct{} { return s.restart }

func (s *Service) SetRadio(on bool) error {
	if !on {
		if err := s.idle(); err != nil {
			return err
		}
	}
	if err := s.ril.SetRadio(on); err != nil {
		return err
	}
	s.mu.Lock()
	s.radioOff = !on
	s.mu.Unlock()
	s.update(func(x *phone.Status) { x.Radio = on })
	if on {
		s.wake()
	}
	return nil
}
