package ril

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

type RIL struct {
	c      *sit.Client
	router string
}

func New(c *sit.Client, router string) *RIL { return &RIL{c: c, router: router} }

func (r *RIL) Client() *sit.Client { return r.c }

func (r *RIL) NRMode() (byte, error) {
	p, err := r.c.Request(sit.GetNRMode, nil, 8*time.Second)
	if err != nil {
		return 0, err
	}
	if len(p) != 1 {
		return 0, errors.New("unexpected NR mode response")
	}
	return p[0], nil
}

func (r *RIL) SetNRMode(mode byte) error {
	if _, err := r.c.Request(sit.SetNRMode, []byte{mode}, 30*time.Second); err != nil {
		return err
	}
	got, err := r.NRMode()
	if err != nil {
		return err
	}
	if got != mode {
		return errors.New("NR mode did not reach the requested value")
	}
	return nil
}

type OperatorInfo struct {
	PLMN string
	Name string
}

func (r *RIL) Operator() (OperatorInfo, error) {
	p, err := r.c.Request(sit.Operator, nil, 8*time.Second)
	if err != nil {
		return OperatorInfo{}, err
	}
	var o OperatorInfo
	if len(p) >= 6 {
		plmn := strings.Trim(strings.ReplaceAll(string(trimZero(p[:6])), "#", ""), " ")
		ok := plmn != ""
		for _, c := range plmn {
			if c < '0' || c > '9' {
				ok = false
			}
		}
		if ok {
			o.PLMN = plmn
		}
	}
	for _, rg := range [][2]int{{6, 38}, {38, 102}} {
		if len(p) < rg[1] {
			continue
		}
		n := strings.TrimSpace(string(bytes.ToValidUTF8(trimZero(p[rg[0]:rg[1]]), nil)))
		printable := n != ""
		for _, c := range n {
			if c < 0x20 {
				printable = false
			}
		}
		if printable {
			o.Name = n
			break
		}
	}
	return o, nil
}

var radioStates = map[uint32]bool{2: true, 10: true}

func (r *RIL) RadioOn() (bool, error) {
	p, err := r.c.Request(sit.GetRadioPower, nil, 5*time.Second)
	if err != nil {
		return false, err
	}
	if len(p) < 4 {
		return false, errors.New("unexpected radio state")
	}
	return radioStates[binary.LittleEndian.Uint32(p)], nil
}

func RadioIndication(payload []byte) (bool, bool) {
	if len(payload) < 4 {
		return false, false
	}
	return radioStates[binary.LittleEndian.Uint32(payload)], true
}

func LTERSRP(payload []byte) (int, bool) {
	if len(payload) < 2+0xc4 || binary.LittleEndian.Uint16(payload)&4 == 0 {
		return 0, false
	}
	v := binary.LittleEndian.Uint32(payload[2+0x40:])
	if v < 44 || v > 140 {
		return 0, false
	}
	return -int(v), true
}

func SignalPercent(rsrp int, ok bool) int {
	if !ok {
		return 0
	}
	p := (rsrp + 140) * 100 / 96
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

func (r *RIL) SetScreen(on bool) error {
	v := uint32(0)
	filter := uint32(0)
	if on {
		v = 1
		filter = 0xffffffff
	}
	if _, err := r.c.Request(sit.SetScreenState, binary.LittleEndian.AppendUint32(nil, v), 5*time.Second); err != nil {
		return err
	}
	_, err := r.c.Request(sit.SetIndFilter, binary.LittleEndian.AppendUint32(nil, filter), 5*time.Second)
	return err
}

func (r *RIL) SMSC() ([]byte, error) {
	p, err := r.c.Request(sit.SMSCAddress, nil, 8*time.Second)
	if err != nil {
		return nil, err
	}
	if len(p) != 13 || p[0] < 1 || p[0] > 12 {
		return nil, errors.New("the carrier SMS centre is unavailable")
	}
	return p, nil
}

func (r *RIL) SendSMS(smsc, pdu []byte) error {
	if len(pdu) > 246 {
		return errors.New("SMS PDU too long")
	}
	p := make([]byte, 260)
	copy(p, smsc)
	p[13] = byte(len(pdu))
	copy(p[14:], pdu)
	res, err := r.c.Request(sit.SendSMS, p, 90*time.Second)
	if err != nil {
		return err
	}
	if len(res) < 253 {
		return errors.New("unknown SMS response")
	}
	if cause := binary.LittleEndian.Uint32(res[249:]); cause != 0 {
		return &sit.ModemError{ID: sit.SendSMS, Code: byte(cause)}
	}
	return nil
}

func (r *RIL) AckSMS(id uint16, reference byte) error {
	ack := uint16(sit.SMSAck)
	if id >= 0x0d00 {
		ack = sit.IMSSMSAck
	}
	p := binary.LittleEndian.AppendUint32(nil, 1)
	p = append(p, reference)
	p = binary.LittleEndian.AppendUint32(p, 0)
	_, err := r.c.Request(ack, p, 8*time.Second)
	return err
}

func SMSPDU(raw []byte) ([]byte, error) {
	if len(raw) < 10 || raw[9] < 1 || raw[9] > 244 || len(raw) < 10+int(raw[9]) {
		return nil, errors.New("invalid incoming SMS layout")
	}
	return raw[10 : 10+int(raw[9])], nil
}

type RegFailure struct {
	Domain     byte
	Cause      int
	Additional int
}

func ParseRegFailure(raw []byte) (RegFailure, error) {
	if len(raw) < 0x11 {
		return RegFailure{}, errors.New("short registration failure")
	}
	f := RegFailure{Domain: raw[0x0e], Cause: -1, Additional: -1}
	if raw[0x0f] != 0xff {
		f.Cause = int(raw[0x0f])
	}
	if raw[0x10] != 0xff {
		f.Additional = int(raw[0x10])
	}
	return f, nil
}
