package ril

import (
	"errors"
	"fmt"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

const (
	legacySlotRecord = 65
	slotRecord       = 105
	portRecord       = 13
	maxSlots         = 4
	unmapped         = 0xff
)

type PhysicalSlot struct {
	Card  byte
	EUICC bool
	Lines []int
}

type SlotStatus struct {
	Ports bool
	Slots []PhysicalSlot
}

type SlotPort struct {
	Slot int
	Port int
}

func ParseSlotStatus(p []byte) (SlotStatus, error) {
	if len(p) < 1+maxSlots*legacySlotRecord || p[0] == 0 || p[0] > maxSlots {
		return SlotStatus{}, errors.New("unexpected slot status layout")
	}
	body := p[1:]
	st := SlotStatus{Ports: len(body) >= maxSlots*slotRecord, Slots: make([]PhysicalSlot, p[0])}
	size := legacySlotRecord
	if st.Ports {
		size = slotRecord
	}
	for i := range st.Slots {
		r := body[i*size : (i+1)*size]
		s := PhysicalSlot{Card: r[0]}
		var lines []byte
		if st.Ports {
			s.EUICC = r[0x23] > 0
			ports := int(r[0x34])
			if ports > maxSlots {
				return SlotStatus{}, errors.New("unexpected slot port count")
			}
			for j := range ports {
				lines = append(lines, r[0x40+j*portRecord])
			}
		} else {
			s.EUICC = r[0x30] > 0
			lines = []byte{r[0x24]}
		}
		for _, l := range lines {
			switch {
			case l == unmapped:
				s.Lines = append(s.Lines, -1)
			case l < maxSlots:
				s.Lines = append(s.Lines, int(l))
			default:
				return SlotStatus{}, errors.New("unexpected logical slot")
			}
		}
		st.Slots[i] = s
	}
	return st, nil
}

func EUICCMapping(st SlotStatus) ([]SlotPort, bool) {
	euicc := -1
	current := map[int]SlotPort{}
	for i, s := range st.Slots {
		if s.EUICC && euicc < 0 {
			euicc = i
		}
		for j, l := range s.Lines {
			if l < 0 {
				continue
			}
			if _, taken := current[l]; taken {
				return nil, false
			}
			current[l] = SlotPort{Slot: i, Port: j}
		}
	}
	if euicc < 0 || st.Slots[euicc].Card != CardPresent || len(st.Slots[euicc].Lines) == 0 {
		return nil, false
	}
	mapping := make([]SlotPort, len(current))
	for l := range mapping {
		sp, ok := current[l]
		if !ok {
			return nil, false
		}
		mapping[l] = sp
	}
	if len(mapping) == 0 || mapping[0].Slot == euicc || st.Slots[mapping[0].Slot].Card != CardAbsent {
		return nil, false
	}
	if l := st.Slots[euicc].Lines[0]; l > 0 {
		mapping[l] = mapping[0]
	}
	mapping[0] = SlotPort{Slot: euicc}
	return mapping, true
}

func mappingPayload(ports bool, mapping []SlotPort) []byte {
	width := 1
	if ports {
		width = 2
	}
	p := make([]byte, 1+maxSlots*width)
	p[0] = byte(len(mapping))
	for l, m := range mapping {
		p[1+l*width] = byte(m.Slot)
		if ports {
			p[2+l*width] = byte(m.Port)
		}
	}
	return p
}

func decodeSlots(p []byte) (SlotStatus, error) {
	st, err := ParseSlotStatus(p)
	if err == nil {
		return st, nil
	}
	if len(p) == 0 {
		return st, fmt.Errorf("%w (empty)", err)
	}
	return st, fmt.Errorf("%w (%d bytes, count %d)", err, len(p), p[0])
}

func readSlots(c *sit.Client) (SlotStatus, error) {
	p, err := c.Request(sit.GetSlotStatus, nil, 8*time.Second)
	if err != nil {
		return SlotStatus{}, err
	}
	return decodeSlots(p)
}

func mapSlots(c *sit.Client, st SlotStatus, logf func(string, ...any)) (bool, error) {
	for i, s := range st.Slots {
		logf("physical slot %d: card %d, euicc %t, lines %v", i, s.Card, s.EUICC, s.Lines)
	}
	mapping, ok := EUICCMapping(st)
	if !ok {
		logf("logical slots left as they are")
		return false, nil
	}
	if _, err := c.Request(sit.SetSlotMapping, mappingPayload(st.Ports, mapping), MappingTimeout); err != nil {
		return false, err
	}
	logf("logical slots mapped to physical %v", mapping)
	return true, nil
}

func (r *RIL) RemapEUICC(table []byte, logf func(string, ...any)) (bool, error) {
	var st SlotStatus
	var err error
	if table == nil {
		st, err = readSlots(r.c)
	} else {
		st, err = decodeSlots(table)
	}
	if err != nil {
		return false, err
	}
	return mapSlots(r.c, st, logf)
}

func MapEUICC(c *sit.Client, wait time.Duration, logf func(string, ...any)) error {
	deadline := time.Now().Add(wait)
	var st SlotStatus
	reported := ""
	for {
		var err error
		if st, err = readSlots(c); err == nil {
			break
		}
		if c.Err() != nil || time.Now().After(deadline) {
			return err
		}
		if msg := err.Error(); msg != reported {
			logf("waiting for the slot table: %v", err)
			reported = msg
		}
		time.Sleep(time.Second)
	}
	if mapped, err := mapSlots(c, st, logf); err != nil || !mapped {
		return err
	}
	deadline = time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if p, err := c.Request(sit.SIMStatus, nil, 8*time.Second); err == nil {
			if st, err := ParseSIMStatus(p); err == nil && st.Card == CardPresent {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return errors.New("the eUICC did not answer on its logical slot")
}
