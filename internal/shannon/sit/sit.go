package sit

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	kindResponse   = 1
	kindIndication = 2
	requestHeader  = 12
	maxFrame       = 32768
)

const (
	CallList           = 0x0000
	SMSAck             = 0x0102
	SMSCAddress        = 0x0108
	IndNewSMS          = 0x010c
	IndNewSMSReport    = 0x010d
	SIMStatus          = 0x0200
	VerifyPIN          = 0x0201
	VerifyPUK          = 0x0202
	SIMIO              = 0x0208
	IndSIMStatus       = 0x0210
	GetSlotStatus      = 0x024d
	SetSlotMapping     = 0x0250
	GetIMSI            = 0x0400
	SetupDataCall      = 0x0600
	DeactDataCall      = 0x0601
	DataCallList       = 0x0602
	InitialAttachAPN   = 0x0603
	IndDataCallList    = 0x0604
	SetDataProfile     = 0x0613
	SetMobileData      = 0x0625
	CSRegState         = 0x0700
	PSRegState         = 0x0701
	Operator           = 0x0702
	IndNetworkState    = 0x070e
	SetPSService       = 0x0710
	GetPSService       = 0x0711
	IndPhysicalChannel = 0x0742
	SetNRMode          = 0x0748
	GetNRMode          = 0x0749
	IndRegFailed       = 0x074a
	SetRadioPower      = 0x0800
	GetRadioPower      = 0x0801
	IndRadioState      = 0x0802
	IndPhoneReset      = 0x0804
	SetScreenState     = 0x0902
	IndSignal          = 0x0906
	SetIndFilter       = 0x0928
	Dial               = 0x0d00
	IndCallRing        = 0x0d01
	Answer             = 0x0d02
	Hangup             = 0x0d03
	IndCallStatus      = 0x0d04
	IndIMSRegistration = 0x0d05
	HiddenMenu         = 0x0d07
	AddPDNInfo         = 0x0d08
	SendDTMF           = 0x0d0a
	SendSMS            = 0x0d1b
	IMSSMSAck          = 0x0d1d
	IndIMSNewSMS       = 0x0d1e
	IndIMSSMSReport    = 0x0d20
	DelPDNInfo         = 0x0d38
	IMSStackStart      = 0x0d39
	IndCarrierStatus   = 0x4601
	IndCarrierSIMInfo  = 0x4602
)

var allowed = map[uint16]bool{
	CallList: true, SMSAck: true, SMSCAddress: true, SIMStatus: true, VerifyPIN: true, VerifyPUK: true,
	GetIMSI: true, SetupDataCall: true, DeactDataCall: true, DataCallList: true, InitialAttachAPN: true,
	SetDataProfile: true, SetMobileData: true, CSRegState: true, PSRegState: true, Operator: true,
	SetPSService: true, GetPSService: true, SetNRMode: true, GetNRMode: true, GetRadioPower: true,
	SetScreenState: true, SetIndFilter: true, Dial: true, Answer: true, Hangup: true, HiddenMenu: true,
	AddPDNInfo: true, SendDTMF: true, SendSMS: true, IMSSMSAck: true, DelPDNInfo: true, IMSStackStart: true,
	SIMIO: true, GetSlotStatus: true, SetSlotMapping: true,
}

var ErrRefused = errors.New("request outside the allow-list")

type ModemError struct {
	ID    uint16
	Code  byte
	Reply []byte
}

func (e *ModemError) Error() string {
	return fmt.Sprintf("modem rejected %s with error %d", Name(e.ID), e.Code)
}

type Indication struct {
	ID      uint16
	Payload []byte
	Raw     []byte
}

type reply struct {
	data []byte
	err  error
}

type Client struct {
	rw         io.ReadWriteCloser
	wmu        sync.Mutex
	rmu        sync.Mutex
	mu         sync.Mutex
	tok        uint32
	wait       map[uint32]chan reply
	ind        chan Indication
	done       chan struct{}
	err        error
	SIMIOCheck func(payload []byte) error
}

func Open(path string) (*Client, error) {
	var rw io.ReadWriteCloser
	if rest, ok := strings.CutPrefix(path, "unix:"); ok {
		c, err := net.Dial("unixpacket", rest)
		if err != nil {
			return nil, err
		}
		rw = c
	} else {
		f, err := os.OpenFile(path, os.O_RDWR|syscall.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		rw = f
	}
	return New(rw), nil
}

func New(rw io.ReadWriteCloser) *Client {
	var seed [4]byte
	if f, err := os.Open("/dev/urandom"); err == nil {
		io.ReadFull(f, seed[:])
		f.Close()
	}
	c := &Client{
		rw:   rw,
		tok:  binary.LittleEndian.Uint32(seed[:]) & 0x7fffffff,
		wait: map[uint32]chan reply{},
		ind:  make(chan Indication, 256),
		done: make(chan struct{}),
	}
	go c.read()
	return c
}

func (c *Client) Indications() <-chan Indication { return c.ind }

func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) Close() error { return c.rw.Close() }

// Err is why the channel closed, or nil while it is open.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
		for t, ch := range c.wait {
			ch <- reply{err: err}
			delete(c.wait, t)
		}
		close(c.done)
	}
	c.mu.Unlock()
}

func (c *Client) read() {
	buf := make([]byte, 0, 65536)
	chunk := make([]byte, 65536)
	for {
		n, err := c.rw.Read(chunk)
		if err != nil {
			c.fail(fmt.Errorf("SIT channel: %w", err))
			close(c.ind)
			return
		}
		buf = append(buf, chunk[:n]...)
		for len(buf) >= 6 {
			kind := buf[0]
			length := int(binary.LittleEndian.Uint16(buf[4:]))
			min := requestHeader
			if kind == kindIndication {
				min = 8
			}
			if (kind != kindResponse && kind != kindIndication) || length < min || length > maxFrame {
				c.fail(errors.New("invalid SIT framing"))
				close(c.ind)
				return
			}
			if len(buf) < length {
				break
			}
			frame := append([]byte(nil), buf[:length]...)
			buf = buf[length:]
			c.dispatch(frame)
		}
		if len(buf) > 65536 {
			c.fail(errors.New("SIT receive limit exceeded"))
			close(c.ind)
			return
		}
		if len(buf) == 0 {
			buf = buf[:0:cap(buf)]
		}
	}
}

func (c *Client) dispatch(frame []byte) {
	id := binary.LittleEndian.Uint16(frame[2:])
	if frame[0] == kindIndication {
		ind := Indication{ID: id, Payload: frame[8:], Raw: frame}
		select {
		case c.ind <- ind:
		default:
			log.Printf("sit: indication queue full, dropped %s", Name(id))
		}
		return
	}
	token := binary.LittleEndian.Uint32(frame[6:])
	c.mu.Lock()
	ch := c.wait[token]
	delete(c.wait, token)
	c.mu.Unlock()
	if ch == nil {
		log.Printf("sit: response %s with an unknown token", Name(id))
		return
	}
	if frame[10] != 0 {
		ch <- reply{err: &ModemError{ID: id, Code: frame[10], Reply: frame[12:]}}
		return
	}
	ch <- reply{data: frame[12:]}
}

func (c *Client) Request(id uint16, payload []byte, timeout time.Duration) ([]byte, error) {
	if !allowed[id] {
		return nil, fmt.Errorf("%s: %w", Name(id), ErrRefused)
	}
	if id == SIMIO {
		if c.SIMIOCheck == nil {
			return nil, fmt.Errorf("%s: %w", Name(id), ErrRefused)
		}
		if err := c.SIMIOCheck(payload); err != nil {
			return nil, err
		}
	}
	if requestHeader+len(payload) > maxFrame {
		return nil, errors.New("SIT request too large")
	}
	c.rmu.Lock()
	defer c.rmu.Unlock()
	ch := make(chan reply, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil, c.err
	}
	c.tok++
	token := c.tok
	c.wait[token] = ch
	c.mu.Unlock()
	frame := make([]byte, requestHeader+len(payload))
	binary.LittleEndian.PutUint16(frame[2:], id)
	binary.LittleEndian.PutUint16(frame[4:], uint16(len(frame)))
	binary.LittleEndian.PutUint32(frame[6:], token)
	copy(frame[requestHeader:], payload)
	c.wmu.Lock()
	n, err := c.rw.Write(frame)
	c.wmu.Unlock()
	if err == nil && n != len(frame) {
		err = errors.New("short SIT write")
	}
	if err != nil {
		c.mu.Lock()
		delete(c.wait, token)
		c.mu.Unlock()
		return nil, err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.data, r.err
	case <-t.C:
		c.mu.Lock()
		delete(c.wait, token)
		c.mu.Unlock()
		return nil, fmt.Errorf("%s: %w", Name(id), os.ErrDeadlineExceeded)
	}
}
