package props

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const (
	areaMagic    = 0x504f5250
	areaVersion  = 0xfc6ed0ab
	headerSize   = 128
	btSize       = 20
	valueMax     = 92
	infoSize     = 4 + valueMax
	longFlag     = 1 << 16
	longErrorMsg = "Must use __system_property_read_callback() to read"
	DefaultSize  = 128 * 1024
)

var ErrFull = errors.New("property area is full")

type Area struct {
	mu   sync.Mutex
	buf  []byte
	used uint32
	info map[string]uint32
}

func NewArea(size int) *Area {
	if size < headerSize+btSize+valueMax {
		size = DefaultSize
	}
	return newArea(make([]byte, size))
}

func newArea(buf []byte) *Area {
	clear(buf)
	a := &Area{buf: buf, info: map[string]uint32{}}
	binary.LittleEndian.PutUint32(a.buf[8:], areaMagic)
	binary.LittleEndian.PutUint32(a.buf[12:], areaVersion)
	a.used = btSize + valueMax
	a.sync()
	return a
}

func (a *Area) Bytes() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]byte(nil), a.buf...)
}

func (a *Area) Get(name string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	off, ok := a.info[name]
	if !ok {
		return "", false
	}
	return a.readValue(off), true
}

func (a *Area) Set(name, value string) error {
	if err := checkName(name); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if off, ok := a.info[name]; ok {
		if len(value) >= valueMax || a.isLong(off) {
			if !strings.HasPrefix(name, "ro.") {
				return fmt.Errorf("property %s: value of %d bytes is too long", name, len(value))
			}
			return fmt.Errorf("property %s is read-only", name)
		}
		a.update(off, value)
		return nil
	}
	if len(value) >= valueMax && !strings.HasPrefix(name, "ro.") {
		return fmt.Errorf("property %s: value of %d bytes is too long", name, len(value))
	}
	return a.add(name, value)
}

func (a *Area) SetAll(values map[string]string) error {
	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := a.Set(n, values[n]); err != nil {
			return err
		}
	}
	return nil
}

func checkName(name string) error {
	if name == "" || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
		return fmt.Errorf("invalid property name %q", name)
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-@:", c)) {
			return fmt.Errorf("invalid property name %q", name)
		}
	}
	return nil
}

func (a *Area) alloc(size int) (uint32, error) {
	aligned := uint32((size + 3) &^ 3)
	if int(a.used)+int(aligned) > len(a.buf)-headerSize {
		return 0, ErrFull
	}
	off := a.used
	a.used += aligned
	return off, nil
}

func (a *Area) data(off uint32) []byte { return a.buf[headerSize+off:] }

func (a *Area) u32(off uint32) uint32 { return binary.LittleEndian.Uint32(a.data(off)) }

func (a *Area) put32(off, v uint32) { binary.LittleEndian.PutUint32(a.data(off), v) }

func (a *Area) newNode(name string) (uint32, error) {
	off, err := a.alloc(btSize + len(name) + 1)
	if err != nil {
		return 0, err
	}
	a.put32(off, uint32(len(name)))
	copy(a.data(off+btSize), name)
	return off, nil
}

func (a *Area) nodeName(off uint32) string {
	n := a.u32(off)
	return string(a.data(off + btSize)[:n])
}

func compare(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func (a *Area) find(slot uint32, name string) (uint32, error) {
	for {
		cur := a.u32(slot)
		if cur == 0 {
			node, err := a.newNode(name)
			if err != nil {
				return 0, err
			}
			a.put32(slot, node)
			return node, nil
		}
		c := compare(name, a.nodeName(cur))
		if c == 0 {
			return cur, nil
		}
		if c < 0 {
			slot = cur + 8
		} else {
			slot = cur + 12
		}
	}
}

func (a *Area) add(name, value string) error {
	saved := a.used
	node := uint32(0)
	for _, seg := range strings.Split(name, ".") {
		n, err := a.find(node+16, seg)
		if err != nil {
			a.used = saved
			return err
		}
		node = n
	}
	if a.u32(node+4) != 0 {
		return fmt.Errorf("property %s already exists", name)
	}
	info, err := a.newInfo(name, value)
	if err != nil {
		a.used = saved
		return err
	}
	a.put32(node+4, info)
	a.info[name] = info
	a.sync()
	binary.LittleEndian.PutUint32(a.buf[4:], binary.LittleEndian.Uint32(a.buf[4:])+1)
	a.wake(info)
	return nil
}

func (a *Area) newInfo(name, value string) (uint32, error) {
	off, err := a.alloc(infoSize + len(name) + 1)
	if err != nil {
		return 0, err
	}
	copy(a.data(off+infoSize), name)
	if len(value) < valueMax {
		a.put32(off, uint32(len(value))<<24)
		copy(a.data(off + 4)[:valueMax], value)
		return off, nil
	}
	long, err := a.alloc(len(value) + 1)
	if err != nil {
		return 0, err
	}
	copy(a.data(long), value)
	a.put32(off, uint32(len(longErrorMsg))<<24|longFlag)
	copy(a.data(off+4), longErrorMsg)
	a.put32(off+4+56, long-off)
	return off, nil
}

func (a *Area) isLong(off uint32) bool { return a.u32(off)&longFlag != 0 }

func (a *Area) readValue(off uint32) string {
	if a.isLong(off) {
		v := a.data(off + a.u32(off+4+56))
		return string(v[:bytes.IndexByte(v, 0)])
	}
	n := a.u32(off) >> 24
	return string(a.data(off + 4)[:n])
}

func (a *Area) update(off uint32, value string) {
	serial := a.u32(off)
	v := a.data(off + 4)[:valueMax]
	backup := a.data(btSize)[:valueMax]
	copy(backup, v)
	serial |= 1
	a.put32(off, serial)
	clear(v)
	copy(v, value)
	a.put32(off, (uint32(len(value))<<24)|((serial+1)&0xffffff))
	binary.LittleEndian.PutUint32(a.buf[4:], binary.LittleEndian.Uint32(a.buf[4:])+1)
	a.wake(off)
}

func (a *Area) sync() {
	binary.LittleEndian.PutUint32(a.buf[0:], a.used)
}
