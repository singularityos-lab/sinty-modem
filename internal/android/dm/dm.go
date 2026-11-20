package dm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

const (
	ioctlSize   = 312
	targetSize  = 40
	nameOffset  = 48
	uuidOffset  = 176
	flagsOffset = 28
	devOffset   = 40

	cmdCreate  = 3
	cmdRemove  = 4
	cmdSuspend = 6
	cmdLoad    = 9

	flagReadOnly = 1 << 0
)

type Target struct {
	Start  uint64
	Length uint64
	Type   string
	Params string
}

type Device struct {
	Name  string
	Major uint32
	Minor uint32
}

func (d Device) Dev() uint64 {
	return uint64(d.Major)<<8 | uint64(d.Minor&0xff) | uint64(d.Minor&^0xff)<<12
}

func request(nr uintptr, size int) uintptr {
	return 3<<30 | uintptr(size)<<16 | 0xfd<<8 | nr
}

func header(name string, size int, flags uint32) ([]byte, error) {
	if len(name) == 0 || len(name) >= 128 || strings.ContainsRune(name, '/') {
		return nil, fmt.Errorf("dm: bad device name %q", name)
	}
	b := make([]byte, size)
	binary.LittleEndian.PutUint32(b[0:], 4)
	binary.LittleEndian.PutUint32(b[4:], 0)
	binary.LittleEndian.PutUint32(b[8:], 0)
	binary.LittleEndian.PutUint32(b[12:], uint32(size))
	binary.LittleEndian.PutUint32(b[16:], ioctlSize)
	binary.LittleEndian.PutUint32(b[flagsOffset:], flags)
	copy(b[nameOffset:nameOffset+127], name)
	return b, nil
}

func call(nr uintptr, b []byte) error {
	f, err := os.OpenFile("/dev/mapper/control", os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), request(nr, ioctlSize), uintptr(unsafe.Pointer(&b[0])))
	if errno != 0 {
		return errno
	}
	return nil
}

func Create(name string, targets []Target, readOnly bool) (Device, error) {
	if len(targets) == 0 {
		return Device{}, errors.New("dm: no targets")
	}
	var flags uint32
	if readOnly {
		flags = flagReadOnly
	}
	b, err := header(name, ioctlSize, flags)
	if err != nil {
		return Device{}, err
	}
	if err := call(cmdCreate, b); err != nil {
		return Device{}, fmt.Errorf("dm create %s: %w", name, err)
	}
	if err := load(name, targets, flags); err != nil {
		Remove(name)
		return Device{}, err
	}
	b, _ = header(name, ioctlSize, flags)
	if err := call(cmdSuspend, b); err != nil {
		Remove(name)
		return Device{}, fmt.Errorf("dm resume %s: %w", name, err)
	}
	dev := binary.LittleEndian.Uint64(b[devOffset:])
	return Device{Name: name, Major: uint32((dev >> 8) & 0xfff), Minor: uint32(dev&0xff | (dev>>12)&^0xff)}, nil
}

func load(name string, targets []Target, flags uint32) error {
	size := ioctlSize
	specs := make([][]byte, len(targets))
	for i, t := range targets {
		if len(t.Type) == 0 || len(t.Type) >= 16 {
			return fmt.Errorf("dm: bad target type %q", t.Type)
		}
		n := targetSize + len(t.Params) + 1
		n = (n + 7) &^ 7
		s := make([]byte, n)
		binary.LittleEndian.PutUint64(s[0:], t.Start)
		binary.LittleEndian.PutUint64(s[8:], t.Length)
		copy(s[24:40], t.Type)
		copy(s[targetSize:], t.Params)
		specs[i] = s
		size += n
	}
	b, err := header(name, size, flags)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(b[20:], uint32(len(targets)))
	off := ioctlSize
	for _, s := range specs {
		binary.LittleEndian.PutUint32(s[20:], uint32(len(s)))
		copy(b[off:], s)
		off += len(s)
	}
	f, err := os.OpenFile("/dev/mapper/control", os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), request(cmdLoad, ioctlSize), uintptr(unsafe.Pointer(&b[0])))
	if errno != 0 {
		return fmt.Errorf("dm load %s: %w", name, errno)
	}
	return nil
}

func Remove(name string) error {
	b, err := header(name, ioctlSize, 0)
	if err != nil {
		return err
	}
	if err := call(cmdRemove, b); err != nil && err != syscall.ENXIO {
		return fmt.Errorf("dm remove %s: %w", name, err)
	}
	return nil
}
