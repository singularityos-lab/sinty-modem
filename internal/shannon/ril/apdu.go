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
	apduHeader  = 26
	apduTimeout = 30 * time.Second
	lastChannel = 19
)

var isdr = []byte{0xa0, 0x00, 0x00, 0x05, 0x59, 0x10, 0x10, 0xff, 0xff, 0xff, 0xff, 0x89, 0x00, 0x00, 0x01, 0x00}

type EUICC struct {
	c       *sit.Client
	channel byte
}

func (r *RIL) EUICC() *EUICC { return &EUICC{c: r.c} }

func (e *EUICC) Connect() error { return nil }

func (e *EUICC) OpenLogicalChannel(aid []byte) (byte, error) {
	if !bytes.Equal(aid, isdr) {
		return 0, errors.New("only the profile manager of the eUICC can be opened")
	}
	if e.channel != 0 {
		return 0, errors.New("the eUICC channel is already open")
	}
	req := make([]byte, 18)
	req[0] = byte(len(aid))
	copy(req[1:17], aid)
	p, err := e.c.Request(sit.OpenChannel, req, apduTimeout)
	if err != nil {
		return 0, err
	}
	if len(p) < 6 {
		return 0, errors.New("unexpected channel layout")
	}
	id := binary.LittleEndian.Uint32(p)
	if id == 0 || id > lastChannel {
		return 0, fmt.Errorf("no logical channel (status %02x%02x)", p[4], p[5])
	}
	e.channel = byte(id)
	if p[4] != 0x90 && p[4] != 0x61 {
		return 0, errors.Join(fmt.Errorf("the profile manager did not answer (status %02x%02x)", p[4], p[5]), e.Disconnect())
	}
	return e.channel, nil
}

func apduRequest(channel byte, cmd []byte) ([]byte, error) {
	if len(cmd) < 4 {
		return nil, errors.New("card command too short")
	}
	var p3 byte
	var data []byte
	if len(cmd) >= 5 {
		p3 = cmd[4]
	}
	if len(cmd) > 5 {
		end := 5 + int(p3)
		if len(cmd) != end && len(cmd) != end+1 {
			return nil, errors.New("card command length does not match")
		}
		data = cmd[5:end]
	}
	p := make([]byte, apduHeader+2*len(data))
	for i, v := range []byte{channel, cmd[0], cmd[1], cmd[2], cmd[3], p3} {
		binary.LittleEndian.PutUint32(p[i*4:], uint32(v))
	}
	binary.LittleEndian.PutUint16(p[24:], uint16(len(data)))
	copy(p[apduHeader:], data)
	return p, nil
}

func (e *EUICC) Transmit(cmd []byte) ([]byte, error) {
	if e.channel == 0 {
		return nil, errors.New("the eUICC channel is closed")
	}
	req, err := apduRequest(e.channel, cmd)
	if err != nil {
		return nil, err
	}
	p, err := e.c.Request(sit.TransmitAPDU, req, apduTimeout)
	if err != nil {
		return nil, err
	}
	if len(p) < 4 {
		return nil, errors.New("unexpected card response layout")
	}
	n := int(binary.LittleEndian.Uint16(p[2:]))
	if n > len(p)-4 {
		return nil, errors.New("card response cut short")
	}
	return append(append([]byte(nil), p[4:4+n]...), p[0], p[1]), nil
}

func (e *EUICC) CloseLogicalChannel(channel byte) error {
	if channel == 0 || channel != e.channel {
		return nil
	}
	if _, err := e.c.Request(sit.CloseChannel, binary.LittleEndian.AppendUint32(nil, uint32(channel)), apduTimeout); err != nil {
		return err
	}
	e.channel = 0
	return nil
}

func (e *EUICC) Disconnect() error { return e.CloseLogicalChannel(e.channel) }
