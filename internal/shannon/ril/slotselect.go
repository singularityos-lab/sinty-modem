package ril

import (
	"errors"
	"fmt"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

const switchableSlots = 2

const MappingTimeout = 30 * time.Second

func SelectSlot(c *sit.Client, slot int, timeout time.Duration) error {
	if slot < 0 || slot >= switchableSlots {
		return fmt.Errorf("no physical slot %d", slot)
	}
	both := []SlotPort{{Slot: slot}, {Slot: switchableSlots - 1 - slot}}
	var refused error
	for _, ports := range []bool{true, false} {
		for _, mapping := range [][]SlotPort{both, both[:1]} {
			_, err := c.Request(sit.SetSlotMapping, mappingPayload(ports, mapping), timeout)
			var me *sit.ModemError
			if !errors.As(err, &me) {
				return err
			}
			refused = err
		}
	}
	return refused
}
