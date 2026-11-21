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
	unmapped         = 0xff
)

type PhysicalSlot struct {
	Card    byte
	EUICC   bool
	Logical int
}

func ParseSlotStatus(p []byte) ([]PhysicalSlot, error) {
	if len(p) < 1 || p[0] == 0 || p[0] > 4 {
		return nil, errors.New("unexpected slot status layout")
	}
	n := int(p[0])
	body := p[1:]
	size := legacySlotRecord
	if len(body) >= 4*slotRecord {
		size = slotRecord
	}
	if len(body) < n*size {
		return nil, errors.New("unexpected slot status layout")
	}
	slots := make([]PhysicalSlot, n)
	for i := range slots {
		r := body[i*size : (i+1)*size]
		s := PhysicalSlot{Card: r[0], Logical: -1}
		var logical byte
		if size == slotRecord {
			s.EUICC = r[0x23] > 0
			logical = unmapped
			if r[0x34] > 0 {
				logical = r[0x36]
			}
		} else {
			s.EUICC = r[0x30] > 0
			logical = r[0x24]
		}
		if logical != unmapped {
			s.Logical = int(logical)
		}
		slots[i] = s
	}
	return slots, nil
}

func EUICCMapping(slots []PhysicalSlot) ([]byte, bool) {
	euicc := -1
	logicals := 0
	for i, s := range slots {
		if s.EUICC && euicc < 0 {
			euicc = i
		}
		if s.Logical >= 0 {
			logicals++
		}
	}
	if euicc < 0 || slots[euicc].Card != CardPresent || slots[euicc].Logical == 0 {
		return nil, false
	}
	for i, s := range slots {
		if i != euicc && s.Logical == 0 && s.Card == CardPresent {
			return nil, false
		}
	}
	if logicals == 0 {
		logicals = 1
	}
	mapping := []byte{byte(euicc)}
	for i := range slots {
		if len(mapping) == logicals {
			break
		}
		if i != euicc {
			mapping = append(mapping, byte(i))
		}
	}
	return mapping, true
}

func MapEUICC(c *sit.Client, wait time.Duration, logf func(string, ...any)) error {
	p, err := c.Request(sit.GetSlotStatus, nil, 8*time.Second)
	if err != nil {
		return err
	}
	slots, err := ParseSlotStatus(p)
	if err != nil {
		return fmt.Errorf("%w: % x", err, p)
	}
	for i, s := range slots {
		logf("physical slot %d: card %d, euicc %t, logical %d", i, s.Card, s.EUICC, s.Logical)
	}
	mapping, ok := EUICCMapping(slots)
	if !ok {
		return nil
	}
	payload := make([]byte, 5)
	payload[0] = byte(len(mapping))
	copy(payload[1:], mapping)
	if _, err := c.Request(sit.SetSlotMapping, payload, 8*time.Second); err != nil {
		return err
	}
	logf("logical slots mapped to physical % d", mapping)
	deadline := time.Now().Add(wait)
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
