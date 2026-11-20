package loop

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	ctlGetFree = 0x4c82
	configure  = 0x4c0a
	clearFd    = 0x4c01

	flagReadOnly  = 1
	flagAutoclear = 4

	configSize = 304
)

type Device struct {
	Path string
	file *os.File
}

func Attach(backing *os.File, offset, size int64, readOnly bool) (*Device, error) {
	ctl, err := os.OpenFile("/dev/loop-control", os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer ctl.Close()
	for attempt := 0; attempt < 8; attempt++ {
		n, _, errno := syscall.Syscall(syscall.SYS_IOCTL, ctl.Fd(), ctlGetFree, 0)
		if errno != 0 {
			return nil, fmt.Errorf("loop: no free device: %w", errno)
		}
		path := fmt.Sprintf("/dev/loop%d", n)
		mode := os.O_RDWR
		if readOnly {
			mode = os.O_RDONLY
		}
		f, err := os.OpenFile(path, mode|syscall.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		cfg := make([]byte, configSize)
		binary.LittleEndian.PutUint32(cfg[0:], uint32(backing.Fd()))
		info := cfg[8:]
		binary.LittleEndian.PutUint64(info[24:], uint64(offset))
		binary.LittleEndian.PutUint64(info[32:], uint64(size))
		flags := uint32(flagAutoclear)
		if readOnly {
			flags |= flagReadOnly
		}
		binary.LittleEndian.PutUint32(info[52:], flags)
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), configure, uintptr(unsafe.Pointer(&cfg[0])))
		if errno == syscall.EBUSY {
			f.Close()
			continue
		}
		if errno != 0 {
			f.Close()
			return nil, fmt.Errorf("loop: configure %s: %w", path, errno)
		}
		return &Device{Path: path, file: f}, nil
	}
	return nil, fmt.Errorf("loop: every free device was taken meanwhile")
}

func (d *Device) Close() error { return d.file.Close() }

func (d *Device) Detach() error {
	syscall.Syscall(syscall.SYS_IOCTL, d.file.Fd(), clearFd, 0)
	return d.file.Close()
}
