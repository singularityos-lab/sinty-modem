package props

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

type Mapped struct {
	*Area
	file *os.File
}

func Create(path string, size int) (*Mapped, error) {
	if size < headerSize+btSize+valueMax {
		size = DefaultSize
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o444)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(int64(size)); err != nil {
		f.Close()
		return nil, err
	}
	buf, err := syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("map %s: %w", path, err)
	}
	return &Mapped{Area: newArea(buf), file: f}, nil
}

func (m *Mapped) Close() error {
	err := syscall.Munmap(m.buf)
	if cerr := m.file.Close(); err == nil {
		err = cerr
	}
	return err
}

func (a *Area) wake(off uint32) {
	if len(a.buf) == 0 {
		return
	}
	futexWake(unsafe.Pointer(&a.buf[headerSize+off]))
	futexWake(unsafe.Pointer(&a.buf[4]))
}

func futexWake(addr unsafe.Pointer) {
	const futexWake = 1
	syscall.Syscall6(syscall.SYS_FUTEX, uintptr(addr), futexWake, 0x7fffffff, 0, 0, 0)
}
