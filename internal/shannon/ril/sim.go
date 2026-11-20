package ril

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

const (
	appRecord = 63

	AppSIM  = 1
	AppUSIM = 2
	AppISIM = 5

	CardAbsent  = 0
	CardPresent = 1
	CardError   = 2
)

type SIMState string

const (
	SIMUnknown  SIMState = "unknown"
	SIMAbsent   SIMState = "absent"
	SIMError    SIMState = "error"
	SIMDetected SIMState = "detected"
	SIMPIN      SIMState = "pin"
	SIMPUK      SIMState = "puk"
	SIMNetLock  SIMState = "network-locked"
	SIMReady    SIMState = "ready"
)

var appStates = map[byte]SIMState{0: SIMUnknown, 1: SIMDetected, 2: SIMPIN, 3: SIMPUK, 4: SIMNetLock, 5: SIMReady}

type SIMApp struct {
	Type       byte
	State      byte
	AID        []byte
	PINLock    byte
	PINRetries int
	PUKRetries int
}

type SIMStatus struct {
	Card byte
	Apps []SIMApp
}

func ParseSIMStatus(p []byte) (SIMStatus, error) {
	if len(p) < 3 || p[2] > 8 || len(p) < 3+int(p[2])*appRecord {
		return SIMStatus{}, errors.New("unexpected SIM status layout")
	}
	st := SIMStatus{Card: p[0]}
	for i := 0; i < int(p[2]); i++ {
		r := p[3+i*appRecord : 3+(i+1)*appRecord]
		app := SIMApp{Type: r[0], State: r[1], PINLock: r[57], PINRetries: int(r[59]), PUKRetries: int(r[60])}
		if n := int(r[3]); n >= 1 && n <= 16 {
			app.AID = append([]byte(nil), r[4:4+n]...)
		}
		st.Apps = append(st.Apps, app)
	}
	return st, nil
}

func (s SIMStatus) Main() *SIMApp {
	for _, want := range []byte{AppUSIM, AppSIM} {
		for i := range s.Apps {
			if s.Apps[i].Type == want {
				return &s.Apps[i]
			}
		}
	}
	if len(s.Apps) > 0 {
		return &s.Apps[0]
	}
	return nil
}

func (s SIMStatus) App(kind byte) *SIMApp {
	for i := range s.Apps {
		if s.Apps[i].Type == kind {
			return &s.Apps[i]
		}
	}
	return nil
}

func (s SIMStatus) State() SIMState {
	switch s.Card {
	case CardAbsent:
		return SIMAbsent
	case CardPresent:
	case CardError:
		return SIMError
	default:
		return SIMUnknown
	}
	app := s.Main()
	if app == nil {
		return SIMDetected
	}
	if st, ok := appStates[app.State]; ok {
		return st
	}
	return SIMUnknown
}

func (s SIMStatus) USIMReady() bool {
	for _, a := range s.Apps {
		if a.Type == AppUSIM && a.State == 5 {
			return true
		}
	}
	return false
}

func (r *RIL) SIMStatus() (SIMStatus, error) {
	p, err := r.c.Request(sit.SIMStatus, nil, 8*time.Second)
	if err != nil {
		return SIMStatus{}, err
	}
	return ParseSIMStatus(p)
}

func (r *RIL) IMSI(aid []byte) (string, error) {
	if len(aid) == 0 || len(aid) > 16 {
		return "", errors.New("no USIM application")
	}
	req := make([]byte, 17)
	req[0] = byte(len(aid))
	copy(req[1:], aid)
	a, err := r.c.Request(sit.GetIMSI, req, 10*time.Second)
	if err != nil {
		return "", err
	}
	if len(a) < 1 || a[0] < 6 || a[0] > 15 || len(a) < 1+int(a[0]) {
		return "", errors.New("unexpected IMSI layout")
	}
	d := a[1 : 1+a[0]]
	for _, c := range d {
		if c < '0' || c > '9' {
			return "", errors.New("unexpected IMSI layout")
		}
	}
	return string(d), nil
}

var ErrWrongPIN = errors.New("wrong PIN")

func pinPayload(code string, aid []byte) ([]byte, error) {
	if len(code) < 4 || len(code) > 8 {
		return nil, errors.New("a PIN has 4 to 8 digits")
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return nil, errors.New("a PIN has only digits")
		}
	}
	p := make([]byte, 26)
	p[0] = byte(len(code))
	copy(p[1:9], code)
	p[9] = byte(len(aid))
	copy(p[10:], aid)
	return p, nil
}

func (r *RIL) VerifyPIN(code string) (int, error) {
	st, err := r.SIMStatus()
	if err != nil {
		return 0, err
	}
	app := st.Main()
	if app == nil || appStates[app.State] != SIMPIN {
		return 0, errors.New("the SIM is not waiting for its PIN")
	}
	p, err := pinPayload(code, app.AID)
	if err != nil {
		return app.PINRetries, err
	}
	_, err = r.c.Request(sit.VerifyPIN, p, 20*time.Second)
	after, serr := r.SIMStatus()
	left := app.PINRetries - 1
	if serr == nil && after.Main() != nil {
		left = after.Main().PINRetries
	}
	var me *sit.ModemError
	if errors.As(err, &me) {
		return left, fmt.Errorf("%w (%d tries left)", ErrWrongPIN, left)
	}
	return left, err
}

func HomePLMN(imsi string) (mcc string, mncs []string) {
	if len(imsi) < 6 {
		return "", nil
	}
	mcc = imsi[:3]
	two, three := imsi[3:5], imsi[3:6]
	if threeDigitMNC[mcc] {
		return mcc, []string{three, two}
	}
	return mcc, []string{two, three}
}

var threeDigitMNC = map[string]bool{
	"302": true, "310": true, "311": true, "312": true, "313": true, "314": true, "315": true, "316": true,
	"334": true, "338": true, "342": true, "344": true, "346": true, "348": true, "354": true, "356": true,
	"358": true, "360": true, "365": true, "366": true, "376": true, "405": true, "708": true, "722": true,
	"732": true, "750": true,
}

func trimZero(b []byte) []byte {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return b[:i]
	}
	return b
}
