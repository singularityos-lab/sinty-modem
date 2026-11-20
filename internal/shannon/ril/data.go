package ril

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

const (
	IMSCID      = 2
	InternetCID = 1
)

var IPTypes = map[string]byte{"ipv4": 1, "ipv6": 2, "ipv4v6": 3}

var APNTypes = map[string]uint32{"default": 1, "mms": 2, "supl": 4, "dun": 8, "hipri": 16, "fota": 32,
	"ims": 64, "cbs": 128, "ia": 256, "emergency": 512, "mcx": 1024, "xcap": 2048}

var apnPattern = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*$`)

func CheckAPN(apn string) error {
	if len(apn) > 99 || !apnPattern.MatchString(apn) {
		return fmt.Errorf("APN %q is not a plain carrier APN", apn)
	}
	return nil
}

type Bearer []byte

func (b Bearer) Status() uint16 { return binary.LittleEndian.Uint16(b) }
func (b Bearer) CID() int       { return int(b[2]) }
func (b Bearer) Active() byte   { return b[3] }
func (b Bearer) PDP() byte      { return b[4] }

func addr(b []byte) (netip.Addr, bool) {
	for _, c := range b {
		if c != 0 {
			a, ok := netip.AddrFromSlice(b)
			return a, ok
		}
	}
	return netip.Addr{}, false
}

func (b Bearer) IPv4() (netip.Addr, bool) { return addr(b[5:9]) }
func (b Bearer) IPv6() (netip.Addr, bool) { return addr(b[9:25]) }

func (b Bearer) DNS() []netip.Addr {
	var out []netip.Addr
	for _, f := range [][2]int{{0x1a, 4}, {0x1e, 16}, {0x2e, 4}, {0x32, 16}} {
		if a, ok := addr(b[f[0] : f[0]+f[1]]); ok {
			out = append(out, a)
		}
	}
	return out
}

func (b Bearer) PCSCF() (v4, v6 [][]byte) {
	add := func(list [][]byte, a []byte) [][]byte {
		zero := true
		for _, c := range a {
			if c != 0 {
				zero = false
			}
		}
		if zero {
			return list
		}
		for _, x := range list {
			if string(x) == string(a) {
				return list
			}
		}
		return append(list, append([]byte(nil), a...))
	}
	if len(b) >= 132 {
		fam := b[0x42]
		if fam == 1 || fam == 3 {
			for _, o := range []int{0x43, 0x47, 0x4b} {
				v4 = add(v4, b[o:o+4])
			}
		}
		if fam == 2 || fam == 3 {
			for _, o := range []int{0x4f, 0x5f, 0x6f} {
				v6 = add(v6, b[o:o+16])
			}
		}
	}
	if len(b) >= 0xea && b[0x84] >= 2 {
		for o := 0x86; o < 0x9a; o += 4 {
			v4 = add(v4, b[o:o+4])
		}
		for o := 0x9a; o < 0xea; o += 16 {
			v6 = add(v6, b[o:o+16])
		}
	}
	return v4, v6
}

func (b Bearer) IMSCapable() bool {
	_, has6 := b.IPv6()
	v4, v6 := b.PCSCF()
	return b.Status() == 0 && (b.Active() == 1 || b.Active() == 2) && (b.PDP() == 2 || b.PDP() == 3) && has6 && len(v4)+len(v6) > 0
}

func (b Bearer) String() string {
	v4, v6 := b.PCSCF()
	return fmt.Sprintf("CID %d PDP %d status %d active %d P-CSCF %d DNS %d", b.CID(), b.PDP(), b.Status(), b.Active(), len(v4)+len(v6), len(b.DNS()))
}

var bearerSizes = map[int]bool{132: true, 234: true, 239: true, 246: true, 280: true, 292: true}

func ParseBearers(p []byte) ([]Bearer, error) {
	if len(p) == 0 || p[0] > 15 {
		return nil, errors.New("invalid data-call list")
	}
	n := int(p[0])
	if n == 0 {
		return nil, nil
	}
	size := (len(p) - 1) / n
	if (len(p)-1)%n != 0 || !bearerSizes[size] {
		return nil, fmt.Errorf("unsupported data-call list layout (%d bytes, %d items)", len(p), n)
	}
	seen := map[int]bool{}
	out := make([]Bearer, n)
	for i := range out {
		out[i] = Bearer(append([]byte(nil), p[1+i*size:1+(i+1)*size]...))
		cid := out[i].CID()
		if cid < 1 || cid > 15 || seen[cid] {
			return nil, errors.New("invalid or duplicate data-call CID")
		}
		seen[cid] = true
	}
	return out, nil
}

func (r *RIL) Bearers() ([]Bearer, error) {
	p, err := r.c.Request(sit.DataCallList, nil, 10*time.Second)
	if err != nil {
		return nil, err
	}
	return ParseBearers(p)
}

var pcscfRequest = map[string]byte{"ipv4": 1, "ipv6": 2, "ipv4v6": 3}

func setupPayload(cid int, rat byte, apn, ipType string, ims bool, apnTypes uint32) ([]byte, error) {
	if err := CheckAPN(apn); err != nil {
		return nil, err
	}
	t, ok := IPTypes[ipType]
	if !ok {
		return nil, fmt.Errorf("IP type %q", ipType)
	}
	full := make([]byte, 983)
	full[12] = byte(cid)
	full[13] = rat
	if ims {
		full[15] = 1
	}
	copy(full[16:], apn)
	full[0xda] = t
	if ims {
		full[0xdb] = pcscfRequest[ipType]
	}
	if apnTypes == 0 {
		apnTypes = 1
		if ims {
			apnTypes = APNTypes["ims"]
		}
	}
	binary.LittleEndian.PutUint32(full[0xf1:], apnTypes)
	full[0xf5] = 2
	binary.LittleEndian.PutUint32(full[0xf6:], 0)
	binary.LittleEndian.PutUint32(full[0xfc:], 0xffffffff)
	binary.LittleEndian.PutUint32(full[0x101:], 0xffffffff)
	full[0x105] = 1
	return full[12:], nil
}

func (r *RIL) InitialAttach(apn, ipType string, ims bool) error {
	p, err := setupPayload(1, 14, apn, ipType, false, 0)
	if err != nil {
		return err
	}
	full := make([]byte, 251)
	copy(full[12:245], p[:233])
	copy(full[0xf1:0xf5], []byte{0, 0, 0, 0})
	full[0xf8] = IPTypes[ipType]
	if ims {
		full[0x0f] = 1
		full[0xdb] = pcscfRequest[ipType]
		full[0xf9] = pcscfRequest[ipType]
	}
	_, err = r.c.Request(sit.InitialAttachAPN, full[12:], 30*time.Second)
	return err
}

type Profile struct {
	ID        uint32
	APN       string
	Types     uint32
	IP        string
	RoamingIP string
	MaxConns  uint32
	MaxTime   uint32
}

func (p Profile) payload() ([]byte, error) {
	if err := CheckAPN(p.APN); err != nil {
		return nil, err
	}
	roaming := p.RoamingIP
	if roaming == "" {
		roaming = p.IP
	}
	full := make([]byte, 246)
	binary.LittleEndian.PutUint32(full[0x0c:], p.ID)
	copy(full[0x10:], p.APN)
	full[0x75] = IPTypes[p.IP]
	full[0x76] = IPTypes[roaming]
	binary.LittleEndian.PutUint32(full[0xdd:], p.MaxTime)
	binary.LittleEndian.PutUint32(full[0xe1:], p.MaxConns)
	full[0xe9] = 1
	binary.LittleEndian.PutUint32(full[0xea:], p.Types)
	return full[12:], nil
}

func (r *RIL) SetProfile(p Profile) error {
	b, err := p.payload()
	if err != nil {
		return err
	}
	_, err = r.c.Request(sit.SetDataProfile, b, 30*time.Second)
	return err
}

func (r *RIL) AllowData() error {
	if _, err := r.c.Request(sit.SetMobileData, []byte{1, 0}, 8*time.Second); err != nil {
		return err
	}
	if _, err := r.c.Request(sit.SetPSService, []byte{1}, 8*time.Second); err != nil {
		return err
	}
	v, err := r.c.Request(sit.GetPSService, nil, 8*time.Second)
	if err != nil {
		return err
	}
	if len(v) != 1 || v[0] != 1 {
		return errors.New("PS service permission is not enabled")
	}
	return nil
}

type Registration struct {
	State  byte
	Reject byte
	RAT    byte
}

func (g Registration) Registered() bool { return g.State == 1 || g.State == 5 }
func (g Registration) Roaming() bool    { return g.State == 5 }

func (r *RIL) PSRegistration() (Registration, error) {
	p, err := r.c.Request(sit.PSRegState, nil, 8*time.Second)
	if err != nil {
		return Registration{}, err
	}
	if len(p) < 4 {
		return Registration{}, errors.New("short registration state")
	}
	return Registration{State: p[0], Reject: p[1], RAT: p[3]}, nil
}

func (r *RIL) Activate(cid int, apn, ipType string, ims bool, apnTypes uint32) (Bearer, error) {
	reg, err := r.PSRegistration()
	if err != nil {
		return nil, err
	}
	if !reg.Registered() {
		return nil, errors.New("PS network is not registered")
	}
	list, err := r.Bearers()
	if err != nil {
		return nil, err
	}
	for _, b := range list {
		if b.CID() == cid {
			return nil, fmt.Errorf("CID %d is already listed by the modem", cid)
		}
	}
	if err := r.AllowData(); err != nil {
		return nil, err
	}
	for i := 0; i < 60; i++ {
		if reg, err = r.PSRegistration(); err == nil && reg.Registered() {
			break
		}
		time.Sleep(time.Second)
	}
	if !reg.Registered() {
		return nil, errors.New("PS network is not registered after allowing data")
	}
	p, err := setupPayload(cid, reg.RAT, apn, ipType, ims, apnTypes)
	if err != nil {
		return nil, err
	}
	res, err := r.c.Request(sit.SetupDataCall, p, 45*time.Second)
	if err != nil {
		return nil, err
	}
	if len(res) < 132 {
		return nil, errors.New("unexpected data-call layout")
	}
	b := Bearer(res)
	log.Printf("ril: setup CID %d: %s", cid, b)
	if b.Status() != 0 || b.CID() != cid || (b.Active() != 1 && b.Active() != 2) {
		return nil, fmt.Errorf("data call CID %d did not become active (status %d)", cid, b.Status())
	}
	return b, nil
}

func (r *RIL) Deactivate(cid int) error {
	_, err := r.c.Request(sit.DeactDataCall, []byte{byte(cid), 0}, 30*time.Second)
	return err
}

func IPTypeFor(protocol string) string {
	switch strings.ToLower(protocol) {
	case "ip", "ipv4":
		return "ipv4"
	case "ipv6":
		return "ipv6"
	}
	return "ipv4v6"
}
