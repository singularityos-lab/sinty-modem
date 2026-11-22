package ril

import (
	"bytes"
	"slices"
	"testing"
)

func slotRecords(size int, cards, eids, logicals []byte) []byte {
	p := []byte{byte(len(cards))}
	for i := range maxSlots {
		r := make([]byte, size)
		if i < len(cards) {
			r[0] = cards[i]
			if size == slotRecord {
				r[0x23] = eids[i]
				r[0x34] = 1
				r[0x35] = 10
				r[0x36] = 0x98
				r[0x40] = logicals[i]
				r[0x41] = 1
			} else {
				r[0x30] = eids[i]
				r[0x24] = logicals[i]
			}
		}
		p = append(p, r...)
	}
	return p
}

func TestSlotStatusLayouts(t *testing.T) {
	for _, size := range []int{legacySlotRecord, slotRecord} {
		st, err := ParseSlotStatus(slotRecords(size, []byte{CardAbsent, CardPresent}, []byte{0, 16}, []byte{0, unmapped}))
		if err != nil {
			t.Fatal(err)
		}
		s := st.Slots
		if st.Ports != (size == slotRecord) || len(s) != 2 || s[0].EUICC || !s[1].EUICC || s[1].Card != CardPresent ||
			!slices.Equal(s[0].Lines, []int{0}) || !slices.Equal(s[1].Lines, []int{-1}) {
			t.Fatalf("size %d: %+v", size, st)
		}
	}
	if _, err := ParseSlotStatus([]byte{2, 0, 0}); err == nil {
		t.Fatal("a short status was accepted")
	}
	if _, err := ParseSlotStatus(slotRecords(slotRecord, []byte{CardPresent}, []byte{16}, []byte{0x98})); err == nil {
		t.Fatal("an impossible logical slot was accepted")
	}
}

func TestTheSecondPortHasItsOwnLine(t *testing.T) {
	p := slotRecords(slotRecord, []byte{CardPresent}, []byte{16}, []byte{0})
	p[1+0x34] = 2
	p[1+0x40+portRecord] = 1
	st, err := ParseSlotStatus(p)
	if err != nil || !slices.Equal(st.Slots[0].Lines, []int{0, 1}) {
		t.Fatalf("%+v %v", st, err)
	}
}

func status(slots ...PhysicalSlot) SlotStatus {
	return SlotStatus{Ports: true, Slots: slots}
}

func TestAnEmptyPhysicalSlotHandsItsLineToTheEUICC(t *testing.T) {
	m, ok := EUICCMapping(status(PhysicalSlot{Card: CardAbsent, Lines: []int{0}}, PhysicalSlot{Card: CardPresent, EUICC: true, Lines: []int{1}}))
	if !ok || !slices.Equal(m, []SlotPort{{Slot: 1}, {Slot: 0}}) {
		t.Fatalf("mapping %v %t", m, ok)
	}
	m, ok = EUICCMapping(status(PhysicalSlot{Card: CardAbsent, Lines: []int{0}}, PhysicalSlot{Card: CardPresent, EUICC: true, Lines: []int{-1}}))
	if !ok || !slices.Equal(m, []SlotPort{{Slot: 1}}) {
		t.Fatalf("single line mapping %v %t", m, ok)
	}
}

func TestLinesAreLeftAloneUnlessTheFirstIsOnAnEmptySlot(t *testing.T) {
	for name, st := range map[string]SlotStatus{
		"physical SIM in use": status(PhysicalSlot{Card: CardPresent, Lines: []int{0}}, PhysicalSlot{Card: CardPresent, EUICC: true, Lines: []int{1}}),
		"already mapped":      status(PhysicalSlot{Card: CardAbsent, Lines: []int{1}}, PhysicalSlot{Card: CardPresent, EUICC: true, Lines: []int{0}}),
		"eUICC without card":  status(PhysicalSlot{Card: CardAbsent, Lines: []int{0}}, PhysicalSlot{Card: CardAbsent, EUICC: true, Lines: []int{1}}),
		"no first line":       status(PhysicalSlot{Card: CardAbsent, Lines: []int{-1}}, PhysicalSlot{Card: CardPresent, EUICC: true, Lines: []int{1}}),
		"a line twice":        status(PhysicalSlot{Card: CardAbsent, Lines: []int{0}}, PhysicalSlot{Card: CardPresent, EUICC: true, Lines: []int{0}}),
		"eUICC without ports": status(PhysicalSlot{Card: CardAbsent, Lines: []int{0}}, PhysicalSlot{Card: CardPresent, EUICC: true}),
	} {
		if m, ok := EUICCMapping(st); ok {
			t.Errorf("%s: mapped to %v", name, m)
		}
	}
}

func TestMappingPayloadsMatchTheStockRIL(t *testing.T) {
	m := []SlotPort{{Slot: 1}, {Slot: 0, Port: 1}}
	if p := mappingPayload(true, m); !bytes.Equal(p, []byte{2, 1, 0, 0, 1, 0, 0, 0, 0}) {
		t.Fatalf("port payload % x", p)
	}
	if p := mappingPayload(false, m); !bytes.Equal(p, []byte{2, 1, 0, 0, 0}) {
		t.Fatalf("legacy payload % x", p)
	}
}
