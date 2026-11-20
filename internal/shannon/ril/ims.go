package ril

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

type IMSRegistration struct {
	State    byte
	Features byte
	Mask     uint32
	Status   uint16
	Reason   string
}

func (g IMSRegistration) Voice() bool { return g.State == 2 && g.Features&1 != 0 }
func (g IMSRegistration) SMS() bool   { return g.State == 2 && g.Features&2 != 0 }

func ParseIMSRegistration(raw []byte) (IMSRegistration, error) {
	if len(raw) < 20 {
		return IMSRegistration{}, errors.New("short IMS registration indication")
	}
	g := IMSRegistration{
		State:    raw[8],
		Features: raw[9],
		Mask:     binary.LittleEndian.Uint32(raw[10:]),
		Status:   binary.LittleEndian.Uint16(raw[14:]),
	}
	uri := int(binary.LittleEndian.Uint16(raw[16:]))
	reason := int(binary.LittleEndian.Uint16(raw[18:]))
	if uri > 1000 || reason > 256 || len(raw) < 20+uri+reason {
		return g, errors.New("truncated IMS registration indication")
	}
	g.Reason = strings.Trim(string(raw[20+uri:20+uri+reason]), "\x00 ")
	return g, nil
}

type Identity struct {
	Manufacturer string
	Model        string
	OS           string
}

func menu(items []byte) []byte {
	out := make([]byte, 3, 3+len(items))
	binary.LittleEndian.PutUint16(out[1:], uint16(len(items)))
	return append(out, items...)
}

func hiddenMenu(id Identity) []byte {
	var items []byte
	for _, tag := range []uint16{523, 527, 529, 531, 533} {
		items = binary.LittleEndian.AppendUint16(items, tag)
		items = binary.LittleEndian.AppendUint32(items, 30)
	}
	clean := func(s string) string {
		var b strings.Builder
		for _, c := range s {
			if c >= 0x20 && c < 0x7f && c != '#' {
				b.WriteRune(c)
			}
		}
		return b.String()
	}
	for _, kv := range []struct {
		tag uint16
		v   string
	}{
		{772, id.Manufacturer}, {773, id.Model}, {775, id.OS}, {776, "Linux"},
		{519, id.Manufacturer + " " + id.Model + " / " + id.OS},
	} {
		items = binary.LittleEndian.AppendUint16(items, kv.tag)
		items = append(items, clean(kv.v)...)
		items = append(items, '#', '#')
	}
	return menu(items)
}

func omaDMMenu() []byte {
	items := binary.LittleEndian.AppendUint16(nil, 777)
	items = binary.LittleEndian.AppendUint32(items, 1)
	return menu(items)
}

func addPDNPayload(b Bearer, cid int, fallback6 [][]byte, pcscfRequired bool) ([]byte, error) {
	if len(b) < 132 {
		return nil, errors.New("short IMS bearer")
	}
	v4, has4 := b.IPv4()
	v6, has6 := b.IPv6()
	var ipType byte
	if (b.PDP() == 1 || b.PDP() == 3) && has4 {
		ipType |= 1
	}
	if (b.PDP() == 2 || b.PDP() == 3) && has6 {
		ipType |= 2
	}
	if b.Status() != 0 || b.CID() != cid || (b.Active() != 1 && b.Active() != 2) || ipType == 0 {
		return nil, errors.New("an active IMS bearer with an address is required")
	}
	p4, p6 := b.PCSCF()
	if len(p4) > 3 {
		p4 = p4[:3]
	}
	if len(p6) > 3 {
		p6 = p6[:3]
	}
	if ipType&1 == 0 {
		p4 = nil
	}
	if ipType&2 == 0 {
		p6 = nil
	}
	if len(p4) == 0 && len(p6) == 0 && ipType&2 != 0 {
		for _, a := range fallback6 {
			if len(a) == 16 && len(p6) < 3 {
				p6 = append(p6, a)
			}
		}
	}
	if len(p4) == 0 && len(p6) == 0 && pcscfRequired {
		return nil, errors.New("the IMS bearer carries no P-CSCF")
	}
	body := []byte{2, ipType}
	if ipType&1 != 0 {
		a := v4.As4()
		body = append(body, a[:]...)
	} else {
		body = append(body, make([]byte, 4)...)
	}
	if ipType&2 != 0 {
		a := v6.As16()
		body = append(body, a[:]...)
	} else {
		body = append(body, make([]byte, 16)...)
	}
	body = append(body, 17, 0, 0, 1, 0, 0)
	body = binary.LittleEndian.AppendUint16(body, 35)
	body = append(body, 0, 0, 1)
	body = append(body, make([]byte, 6)...)
	body = append(body, byte(len(p4)))
	for _, a := range p4 {
		body = append(body, a...)
	}
	body = append(body, byte(len(p6)))
	for _, a := range p6 {
		body = append(body, a...)
	}
	for _, at := range [][2]int{{0x1a, 0x1e}, {0x2e, 0x32}} {
		if ipType&1 != 0 {
			body = append(body, b[at[0]:at[0]+4]...)
		} else {
			body = append(body, make([]byte, 4)...)
		}
		if ipType&2 != 0 {
			body = append(body, b[at[1]:at[1]+16]...)
		} else {
			body = append(body, make([]byte, 16)...)
		}
	}
	body = append(body, make([]byte, 23+4+10+4+2+4+1+32)...)
	body = binary.LittleEndian.AppendUint16(body, 12)
	body = binary.LittleEndian.AppendUint16(body, 1)
	body = binary.LittleEndian.AppendUint16(body, 8)
	body = binary.LittleEndian.AppendUint64(body, 0)
	body = append(body, make([]byte, 488)...)
	return body, nil
}

func (r *RIL) StartIMSStack() error {
	_, err := r.c.Request(sit.IMSStackStart, []byte{1, 1}, 30*time.Second)
	return err
}

func (r *RIL) RegisterIMS(b Bearer, cid int, id Identity, fallback6 [][]byte) error {
	body, err := addPDNPayload(b, cid, fallback6, true)
	if err != nil {
		return err
	}
	if _, err := r.c.Request(sit.HiddenMenu, hiddenMenu(id), 30*time.Second); err != nil {
		return fmt.Errorf("IMS hidden menu: %w", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := r.c.Request(sit.HiddenMenu, omaDMMenu(), 30*time.Second); err != nil {
		return fmt.Errorf("IMS OMA-DM menu: %w", err)
	}
	if _, err := r.c.Request(sit.AddPDNInfo, body, 30*time.Second); err != nil {
		return fmt.Errorf("IMS PDN information: %w", err)
	}
	return nil
}

func (r *RIL) UnregisterIMS() error {
	_, err := r.c.Request(sit.DelPDNInfo, []byte{1, 2, 0}, 30*time.Second)
	return err
}
