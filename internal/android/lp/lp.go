package lp

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	reservedBytes = 4096
	geometrySize  = 4096
	geometryMagic = 0x616c4467
	headerMagic   = 0x414c5030
	SectorSize    = 512

	TargetLinear = 0
	TargetZero   = 1
)

type Extent struct {
	Sectors uint64
	Type    uint32
	Data    uint64
	Source  uint32
}

type Partition struct {
	Name       string
	Attributes uint32
	Group      uint32
	Extents    []Extent
}

func (p Partition) Sectors() uint64 {
	var n uint64
	for _, e := range p.Extents {
		n += e.Sectors
	}
	return n
}

type BlockDevice struct {
	Name        string
	FirstSector uint64
	Size        uint64
}

type Metadata struct {
	Major, Minor uint16
	Partitions   []Partition
	BlockDevices []BlockDevice
}

func (m *Metadata) Find(name string) (Partition, bool) {
	for _, p := range m.Partitions {
		if p.Name == name {
			return p, true
		}
	}
	return Partition{}, false
}

type geometry struct {
	maxSize   uint32
	slots     uint32
	blockSize uint32
}

func parseGeometry(b []byte) (geometry, error) {
	if len(b) < 52 || binary.LittleEndian.Uint32(b) != geometryMagic {
		return geometry{}, errors.New("lp: no metadata geometry")
	}
	size := binary.LittleEndian.Uint32(b[4:])
	if size < 52 || int(size) > len(b) {
		return geometry{}, errors.New("lp: bad geometry size")
	}
	g := make([]byte, size)
	copy(g, b[:size])
	want := append([]byte(nil), g[8:40]...)
	clear(g[8:40])
	if sum := sha256.Sum256(g); !bytes.Equal(sum[:], want) {
		return geometry{}, errors.New("lp: geometry checksum mismatch")
	}
	geo := geometry{
		maxSize:   binary.LittleEndian.Uint32(b[40:]),
		slots:     binary.LittleEndian.Uint32(b[44:]),
		blockSize: binary.LittleEndian.Uint32(b[48:]),
	}
	if geo.maxSize == 0 || geo.maxSize%SectorSize != 0 || geo.slots == 0 || geo.slots > 3 {
		return geometry{}, errors.New("lp: implausible geometry")
	}
	return geo, nil
}

type table struct{ offset, count, size uint32 }

func (t table) entries(tables []byte, min uint32) ([][]byte, error) {
	if t.count == 0 {
		return nil, nil
	}
	if t.size < min {
		return nil, fmt.Errorf("lp: table entry of %d bytes, want at least %d", t.size, min)
	}
	end := uint64(t.offset) + uint64(t.count)*uint64(t.size)
	if end > uint64(len(tables)) {
		return nil, errors.New("lp: table outside the metadata")
	}
	out := make([][]byte, t.count)
	for i := range out {
		o := t.offset + uint32(i)*t.size
		out[i] = tables[o : o+t.size]
	}
	return out, nil
}

func cname(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func parseMetadata(b []byte) (*Metadata, error) {
	if len(b) < 128 || binary.LittleEndian.Uint32(b) != headerMagic {
		return nil, errors.New("lp: no metadata header")
	}
	m := &Metadata{Major: binary.LittleEndian.Uint16(b[4:]), Minor: binary.LittleEndian.Uint16(b[6:])}
	if m.Major != 10 {
		return nil, fmt.Errorf("lp: unsupported metadata version %d.%d", m.Major, m.Minor)
	}
	hsize := binary.LittleEndian.Uint32(b[8:])
	if hsize < 128 || int(hsize) > len(b) {
		return nil, errors.New("lp: bad header size")
	}
	h := append([]byte(nil), b[:hsize]...)
	want := append([]byte(nil), h[12:44]...)
	clear(h[12:44])
	if sum := sha256.Sum256(h); !bytes.Equal(sum[:], want) {
		return nil, errors.New("lp: header checksum mismatch")
	}
	tsize := binary.LittleEndian.Uint32(b[44:])
	if uint64(hsize)+uint64(tsize) > uint64(len(b)) {
		return nil, errors.New("lp: tables outside the metadata")
	}
	tables := b[hsize : hsize+tsize]
	if sum := sha256.Sum256(tables); !bytes.Equal(sum[:], b[48:80]) {
		return nil, errors.New("lp: tables checksum mismatch")
	}
	desc := func(o int) table {
		return table{binary.LittleEndian.Uint32(b[o:]), binary.LittleEndian.Uint32(b[o+4:]), binary.LittleEndian.Uint32(b[o+8:])}
	}
	parts, err := desc(80).entries(tables, 52)
	if err != nil {
		return nil, err
	}
	exts, err := desc(92).entries(tables, 24)
	if err != nil {
		return nil, err
	}
	devs, err := desc(116).entries(tables, 64)
	if err != nil {
		return nil, err
	}
	extents := make([]Extent, len(exts))
	for i, e := range exts {
		extents[i] = Extent{
			Sectors: binary.LittleEndian.Uint64(e),
			Type:    binary.LittleEndian.Uint32(e[8:]),
			Data:    binary.LittleEndian.Uint64(e[12:]),
			Source:  binary.LittleEndian.Uint32(e[20:]),
		}
	}
	for _, d := range devs {
		m.BlockDevices = append(m.BlockDevices, BlockDevice{
			FirstSector: binary.LittleEndian.Uint64(d),
			Size:        binary.LittleEndian.Uint64(d[16:]),
			Name:        cname(d[24:60]),
		})
	}
	for _, p := range parts {
		first := binary.LittleEndian.Uint32(p[40:])
		n := binary.LittleEndian.Uint32(p[44:])
		if uint64(first)+uint64(n) > uint64(len(extents)) {
			return nil, errors.New("lp: partition extents outside the table")
		}
		part := Partition{
			Name:       cname(p[:36]),
			Attributes: binary.LittleEndian.Uint32(p[36:]),
			Group:      binary.LittleEndian.Uint32(p[48:]),
			Extents:    append([]Extent(nil), extents[first:first+n]...),
		}
		for _, e := range part.Extents {
			if e.Type == TargetLinear && int(e.Source) >= len(m.BlockDevices) {
				return nil, errors.New("lp: extent on an unknown block device")
			}
		}
		m.Partitions = append(m.Partitions, part)
	}
	return m, nil
}

func readAt(r io.ReaderAt, off int64, n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := r.ReadAt(b, off); err != nil {
		return nil, err
	}
	return b, nil
}

func ReadDevice(r io.ReaderAt, slot int) (*Metadata, error) {
	var geo geometry
	var gerr error
	for _, off := range []int64{reservedBytes, reservedBytes + geometrySize} {
		b, err := readAt(r, off, geometrySize)
		if err != nil {
			gerr = err
			continue
		}
		if geo, gerr = parseGeometry(b); gerr == nil {
			break
		}
	}
	if gerr != nil {
		return nil, gerr
	}
	if slot < 0 || uint32(slot) >= geo.slots {
		return nil, fmt.Errorf("lp: no metadata slot %d", slot)
	}
	base := int64(reservedBytes + 2*geometrySize)
	var merr error
	for _, off := range []int64{
		base + int64(slot)*int64(geo.maxSize),
		base + int64(geo.slots)*int64(geo.maxSize) + int64(slot)*int64(geo.maxSize),
	} {
		b, err := readAt(r, off, int(geo.maxSize))
		if err != nil {
			merr = err
			continue
		}
		m, err := parseMetadata(b)
		if err == nil {
			return m, nil
		}
		merr = err
	}
	return nil, merr
}

func ReadImage(b []byte) (*Metadata, error) {
	geo, err := parseGeometry(b)
	if err != nil {
		return nil, err
	}
	end := len(b)
	if end > geometrySize+int(geo.maxSize) {
		end = geometrySize + int(geo.maxSize)
	}
	return parseMetadata(b[geometrySize:end])
}
