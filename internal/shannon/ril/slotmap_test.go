package ril

import (
	"bytes"
	"testing"
)

func slotRecords(size int, cards, eids, logicals []byte) []byte {
	p := []byte{byte(len(cards))}
	for i := 0; i < 4 && (size == slotRecord || i < len(cards)); i++ {
		r := make([]byte, size)
		if i < len(cards) {
			r[0] = cards[i]
			if size == slotRecord {
				r[0x23] = eids[i]
				r[0x34] = 1
				r[0x36] = logicals[i]
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
		slots, err := ParseSlotStatus(slotRecords(size, []byte{CardAbsent, CardPresent}, []byte{0, 16}, []byte{0, unmapped}))
		if err != nil {
			t.Fatal(err)
		}
		if len(slots) != 2 || slots[0].EUICC || !slots[1].EUICC || slots[0].Logical != 0 || slots[1].Logical != -1 || slots[1].Card != CardPresent {
			t.Fatalf("size %d: %+v", size, slots)
		}
	}
	if _, err := ParseSlotStatus([]byte{2, 0, 0}); err == nil {
		t.Fatal("a short status was accepted")
	}
}

func TestAnEmptyPhysicalSlotHandsItsLineToTheEUICC(t *testing.T) {
	slots := []PhysicalSlot{{Card: CardAbsent, Logical: 0}, {Card: CardPresent, EUICC: true, Logical: 1}}
	m, ok := EUICCMapping(slots)
	if !ok || !bytes.Equal(m, []byte{1, 0}) {
		t.Fatalf("mapping %v %t", m, ok)
	}
	slots = []PhysicalSlot{{Card: CardAbsent, Logical: 0}, {Card: CardPresent, EUICC: true, Logical: -1}}
	if m, ok = EUICCMapping(slots); !ok || !bytes.Equal(m, []byte{1}) {
		t.Fatalf("single line mapping %v %t", m, ok)
	}
}

func TestAPhysicalSIMInUseKeepsItsLine(t *testing.T) {
	if _, ok := EUICCMapping([]PhysicalSlot{{Card: CardPresent, Logical: 0}, {Card: CardPresent, EUICC: true, Logical: 1}}); ok {
		t.Fatal("a present physical SIM lost its line")
	}
	if _, ok := EUICCMapping([]PhysicalSlot{{Card: CardAbsent, Logical: 1}, {Card: CardPresent, EUICC: true, Logical: 0}}); ok {
		t.Fatal("an eUICC already on the first line was mapped again")
	}
	if _, ok := EUICCMapping([]PhysicalSlot{{Card: CardAbsent, Logical: 0}, {Card: CardAbsent, EUICC: true, Logical: 1}}); ok {
		t.Fatal("an eUICC without a card was mapped")
	}
}
