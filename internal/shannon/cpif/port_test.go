package cpif

import (
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
	"time"
)

func pair(t *testing.T) (*Port, int) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Skipf("no socket pairs: %v", err)
	}
	p, err := newPort(fds[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, fds[1]
}

func TestAnEmptyReadIsNotTheEndOfTheChannel(t *testing.T) {
	p, peer := pair(t)
	defer syscall.Close(peer)
	go func() {
		syscall.Write(peer, nil)
		time.Sleep(300 * time.Millisecond)
		syscall.Write(peer, []byte("data"))
	}()
	b := make([]byte, 16)
	n, err := p.Read(b)
	if err != nil || string(b[:n]) != "data" {
		t.Fatalf("read %q %v", b[:n], err)
	}
}

func TestAHangupEndsTheChannel(t *testing.T) {
	p, peer := pair(t)
	syscall.Close(peer)
	if _, err := p.Read(make([]byte, 16)); err != io.EOF {
		t.Fatalf("read after hangup: %v", err)
	}
}

func TestTheDeadlineEndsAQuietRead(t *testing.T) {
	p, peer := pair(t)
	defer syscall.Close(peer)
	p.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, err := p.Read(make([]byte, 16)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("quiet read: %v", err)
	}
}

func TestCloseReleasesAWaitingRead(t *testing.T) {
	p, peer := pair(t)
	defer syscall.Close(peer)
	done := make(chan error, 1)
	go func() {
		_, err := p.Read(make([]byte, 16))
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	p.Close()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("read after close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close left the read waiting")
	}
}
