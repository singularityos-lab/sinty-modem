package ril

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

const (
	radioPowerOff = 1
	radioPowerOn  = 2
)

func SetStandby(c *sit.Client, dual bool, timeout time.Duration) error {
	p := []byte{0}
	if dual {
		p[0] = 1
	}
	_, err := c.Request(sit.SetStandby, p, timeout)
	return err
}

func SetDataLine(c *sit.Client, line int, timeout time.Duration) error {
	if line < 0 || line >= maxSlots {
		return fmt.Errorf("no line %d", line)
	}
	_, err := c.Request(sit.SetDataLine, []byte{byte(line)}, timeout)
	return err
}

func (r *RIL) SetLinePower(on bool) error {
	p := make([]byte, 6)
	state := uint32(radioPowerOff)
	if on {
		state = radioPowerOn
	}
	binary.LittleEndian.PutUint32(p, state)
	_, err := r.c.Request(sit.SetRadioPower, p, 25*time.Second)
	return err
}

func EUICCLine(st SlotStatus) (int, bool) {
	for _, s := range st.Slots {
		if !s.EUICC || s.Card != CardPresent {
			continue
		}
		for _, l := range s.Lines {
			if l >= 0 {
				return l, true
			}
		}
	}
	return -1, false
}

func ServeEUICCLine(c *sit.Client, standbyFile string, logf func(string, ...any)) error {
	st, err := readSlots(c)
	if err != nil {
		return err
	}
	line, ok := EUICCLine(st)
	if !ok {
		return errors.New("no eUICC with a card on any line")
	}
	if line == 0 {
		return nil
	}
	if _, err := os.Stat(standbyFile); err != nil {
		if err := SetStandby(c, true, MappingTimeout); err != nil {
			return fmt.Errorf("both lines on standby: %w", err)
		}
		if err := os.WriteFile(standbyFile, []byte("dual\n"), 0o644); err != nil {
			logf("the standby choice was not saved: %v", err)
		}
		logf("both lines on standby")
	}
	if err := SetDataLine(c, line, MappingTimeout); err != nil {
		return fmt.Errorf("mobile data on line %d: %w", line, err)
	}
	logf("mobile data on line %d", line)
	return nil
}
