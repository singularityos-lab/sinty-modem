package audio

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	hwParamsSize = 608
	swParamsSize = 136

	ioctlHWParams = 0xc2604111
	ioctlSWParams = 0xc0884113
	ioctlPrepare  = 0x4140
	ioctlStart    = 0x4142
	ioctlDrop     = 0x4143

	paramAccess     = 0
	paramFormat     = 1
	paramSubformat  = 2
	paramChannels   = 10
	paramRate       = 11
	paramPeriodSize = 13
	paramPeriods    = 15

	accessRWInterleaved = 3
	formatS16LE         = 2

	maskOffset     = 4
	maskSize       = 32
	intervalOffset = 4 + 8*maskSize
	intervalSize   = 12
	rmaskOffset    = intervalOffset + 21*intervalSize
)

type PCM struct {
	f *os.File
}

func ioctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

func setMask(b []byte, param int, bit uint) {
	o := maskOffset + param*maskSize
	clear(b[o : o+maskSize])
	binary.LittleEndian.PutUint32(b[o+int(bit/32)*4:], 1<<(bit%32))
}

func setInterval(b []byte, param int, v uint32) {
	o := intervalOffset + (param-8)*intervalSize
	binary.LittleEndian.PutUint32(b[o:], v)
	binary.LittleEndian.PutUint32(b[o+4:], v)
	binary.LittleEndian.PutUint32(b[o+8:], 1<<2)
}

func hwParams(channels, rate, period, periods uint32) []byte {
	b := make([]byte, hwParamsSize)
	for p := 0; p < 8; p++ {
		o := maskOffset + p*maskSize
		for i := 0; i < maskSize; i++ {
			b[o+i] = 0xff
		}
	}
	for p := 0; p < 21; p++ {
		o := intervalOffset + p*intervalSize
		binary.LittleEndian.PutUint32(b[o+4:], 0xffffffff)
	}
	setMask(b, paramAccess, accessRWInterleaved)
	setMask(b, paramFormat, formatS16LE)
	setMask(b, paramSubformat, 0)
	setInterval(b, paramChannels, channels)
	setInterval(b, paramRate, rate)
	if period > 0 {
		setInterval(b, paramPeriodSize, period)
	}
	if periods > 0 {
		setInterval(b, paramPeriods, periods)
	}
	binary.LittleEndian.PutUint32(b[rmaskOffset:], 0xffffffff)
	return b
}

func OpenPCM(card, device int, capture bool, channels, rate, period, periods uint32) (*PCM, error) {
	dir := 'p'
	if capture {
		dir = 'c'
	}
	path := fmt.Sprintf("/dev/snd/pcmC%dD%d%c", card, device, dir)
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	p := &PCM{f: f}
	hw := hwParams(channels, rate, period, periods)
	if err := ioctl(f, ioctlHWParams, unsafe.Pointer(&hw[0])); err != nil {
		hw = hwParams(channels, rate, 0, 0)
		if err2 := ioctl(f, ioctlHWParams, unsafe.Pointer(&hw[0])); err2 != nil {
			f.Close()
			return nil, fmt.Errorf("%s: hardware parameters: %w", path, err)
		}
		period = binary.LittleEndian.Uint32(hw[intervalOffset+(paramPeriodSize-8)*intervalSize:])
	}
	sw := make([]byte, swParamsSize)
	binary.LittleEndian.PutUint64(sw[16:], uint64(period))
	binary.LittleEndian.PutUint64(sw[24:], 1)
	binary.LittleEndian.PutUint64(sw[32:], 1)
	binary.LittleEndian.PutUint64(sw[40:], ^uint64(0)>>1)
	binary.LittleEndian.PutUint32(sw[72:], 0x00020004)
	if err := ioctl(f, ioctlSWParams, unsafe.Pointer(&sw[0])); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: software parameters: %w", path, err)
	}
	if err := ioctl(f, ioctlPrepare, nil); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: prepare: %w", path, err)
	}
	if err := ioctl(f, ioctlStart, nil); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: start: %w", path, err)
	}
	return p, nil
}

func (p *PCM) Close() error {
	ioctl(p.f, ioctlDrop, nil)
	return p.f.Close()
}
