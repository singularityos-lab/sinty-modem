package props

import (
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

const (
	msgSetProp    = 1
	msgSetProp2   = 0x00020001
	resultSuccess = 0
	resultInvalid = 0x10
	resultFailed  = 0x18

	legacyNameMax = 32
)

type Service struct {
	area     *Area
	listener *net.UnixListener
	Observe  func(name, value string)
}

func Serve(area *Area, path string) (*Service, error) {
	os.Remove(path)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o666); err != nil {
		l.Close()
		return nil, err
	}
	s := &Service{area: area, listener: l}
	go s.accept()
	return s, nil
}

func (s *Service) Close() error { return s.listener.Close() }

func (s *Service) accept() {
	for {
		c, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *Service) handle(c *net.UnixConn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	var cmd uint32
	if err := binary.Read(c, binary.LittleEndian, &cmd); err != nil {
		return
	}
	switch cmd {
	case msgSetProp2:
		name, err1 := readString(c)
		value, err2 := readString(c)
		if err1 != nil || err2 != nil {
			binary.Write(c, binary.LittleEndian, uint32(resultInvalid))
			return
		}
		binary.Write(c, binary.LittleEndian, uint32(s.set(name, value)))
	case msgSetProp:
		buf := make([]byte, legacyNameMax+valueMax)
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		s.set(cstring(buf[:legacyNameMax]), cstring(buf[legacyNameMax:]))
	}
}

func (s *Service) set(name, value string) int {
	if strings.HasPrefix(name, "ctl.") {
		log.Printf("props: ignoring %s=%s", name, value)
		return resultSuccess
	}
	if err := s.area.Set(name, value); err != nil {
		log.Printf("props: %v", err)
		return resultFailed
	}
	if s.Observe != nil {
		s.Observe(name, value)
	}
	return resultSuccess
}

func readString(r io.Reader) (string, error) {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return "", err
	}
	if n > 4096 {
		return "", errors.New("property string too long")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
