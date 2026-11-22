package cpif

import (
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	pollIn      = 0x01
	pollErr     = 0x08
	pollHangup  = 0x10
	pollInvalid = 0x20
)

type pollFD struct {
	fd      int32
	events  int16
	revents int16
}

type Port struct {
	fd       int
	wake     [2]int
	mu       sync.RWMutex
	closed   atomic.Bool
	deadline atomic.Int64
}

func Open(path string) (*Port, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return newPort(fd)
}

func newPort(fd int) (*Port, error) {
	p := &Port{fd: fd}
	if err := syscall.Pipe2(p.wake[:], syscall.O_CLOEXEC); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return p, nil
}

func (p *Port) SetReadDeadline(t time.Time) error {
	var v int64
	if !t.IsZero() {
		v = t.UnixNano()
	}
	p.deadline.Store(v)
	return nil
}

// DEVICE-FIX(gs201): cpif read returns 0 after 100 ms without data, so only a hangup from poll ends the stream
func (p *Port) Read(b []byte) (int, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for {
		if p.closed.Load() {
			return 0, os.ErrClosed
		}
		var ts *syscall.Timespec
		if d := p.deadline.Load(); d != 0 {
			left := time.Until(time.Unix(0, d))
			if left <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			v := syscall.NsecToTimespec(int64(left))
			ts = &v
		}
		fds := [2]pollFD{{fd: int32(p.fd), events: pollIn}, {fd: int32(p.wake[0]), events: pollIn}}
		_, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&fds[0])), uintptr(len(fds)), uintptr(unsafe.Pointer(ts)), 0, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return 0, os.NewSyscallError("ppoll", errno)
		}
		if fds[1].revents != 0 {
			return 0, os.ErrClosed
		}
		ev := fds[0].revents
		if ev&pollIn != 0 {
			n, err := syscall.Read(p.fd, b)
			if err != nil && err != syscall.EINTR && err != syscall.EAGAIN {
				return 0, os.NewSyscallError("read", err)
			}
			if n > 0 {
				return n, nil
			}
		}
		if ev&(pollErr|pollHangup|pollInvalid) != 0 {
			return 0, io.EOF
		}
	}
}

func (p *Port) Write(b []byte) (int, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed.Load() {
		return 0, os.ErrClosed
	}
	for {
		n, err := syscall.Write(p.fd, b)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return 0, os.NewSyscallError("write", err)
		}
		return n, nil
	}
}

func (p *Port) Close() error {
	if p.closed.Swap(true) {
		return os.ErrClosed
	}
	syscall.Write(p.wake[1], []byte{0})
	p.mu.Lock()
	defer p.mu.Unlock()
	syscall.Close(p.wake[0])
	syscall.Close(p.wake[1])
	return syscall.Close(p.fd)
}
