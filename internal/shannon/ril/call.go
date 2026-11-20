package ril

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

const (
	CallActive     = 0
	CallHeld       = 1
	CallDialing    = 2
	CallAlerting   = 3
	CallIncoming   = 4
	CallWaiting    = 5
	CallTerminated = 6
	CallHeld2      = 7
	CallActive2    = 8

	CauseNormal = 16
)

type CallStatus struct {
	Type  uint16
	ID    byte
	State byte
	Cause byte
}

func ParseCallStatus(raw []byte) (CallStatus, error) {
	if len(raw) < 19 {
		return CallStatus{}, errors.New("short IMS call indication")
	}
	s := CallStatus{Type: binary.LittleEndian.Uint16(raw[8:]), ID: raw[10], State: raw[11], Cause: raw[15]}
	if s.State > 8 || raw[12] != 0 || raw[13] > 1 {
		return s, fmt.Errorf("unsupported IMS call status (state %d, multiparty %d, users %d)", s.State, raw[12], raw[13])
	}
	return s, nil
}

type Ring struct {
	Type   uint16
	ID     byte
	Caller string
}

func ParseRing(raw []byte) (Ring, error) {
	if len(raw) < 13 {
		return Ring{}, errors.New("short IMS ring indication")
	}
	r := Ring{Type: binary.LittleEndian.Uint16(raw[8:]), ID: raw[10]}
	n := int(binary.LittleEndian.Uint16(raw[11:]))
	if n > 513 || len(raw) < 13+n {
		return r, nil
	}
	r.Caller = string(bytes.TrimRight(raw[13:13+n], "\x00"))
	return r, nil
}

func dialPayload(number string) ([]byte, error) {
	if len(number) < 1 || len(number) > 513 {
		return nil, errors.New("invalid number length")
	}
	p := make([]byte, 679)
	binary.LittleEndian.PutUint16(p[0:], 1)
	p[2] = 0
	binary.LittleEndian.PutUint16(p[3:], uint16(len(number)))
	copy(p[5:], number)
	return p, nil
}

func (r *RIL) Dial(number string) error {
	p, err := dialPayload(number)
	if err != nil {
		return err
	}
	_, err = r.c.Request(sit.Dial, p, 8*time.Second)
	return err
}

func (r *RIL) Answer() error {
	p := make([]byte, 6)
	binary.LittleEndian.PutUint16(p[0:], 1)
	binary.LittleEndian.PutUint16(p[2:], 1)
	_, err := r.c.Request(sit.Answer, p, 8*time.Second)
	return err
}

func (r *RIL) Hangup(id byte) error {
	_, err := r.c.Request(sit.Hangup, []byte{id, 1, 0}, 8*time.Second)
	return err
}

func (r *RIL) DTMF(id byte, key byte) error {
	_, err := r.c.Request(sit.SendDTMF, []byte{id, 0, key}, 8*time.Second)
	return err
}

func (r *RIL) CallsIdle() (bool, error) {
	p, err := r.c.Request(sit.CallList, nil, 8*time.Second)
	if err != nil {
		return false, err
	}
	return bytes.Equal(p, make([]byte, 4)) || (len(p) > 0 && p[0] == 0), nil
}
