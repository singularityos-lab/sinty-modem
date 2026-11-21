package ril

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

type Slot struct {
	Name string
	Path string
}

type Opener func(path string) (*sit.Client, error)

// probe asks the slot's channel for the card state until the card answers or
// wait runs out. A channel the modem closes while it is still settling is
// opened again, so a probe that starts early still reaches the card. The
// client it returns is open, whatever the outcome, unless the error says the
// channel could not be opened at all.
func probe(ctx context.Context, s Slot, open Opener, wait time.Duration) (*sit.Client, SIMStatus, error) {
	deadline := time.Now().Add(wait)
	var last error
	var c *sit.Client
	for {
		if c == nil {
			var err error
			if c, err = open(s.Path); err != nil {
				return nil, SIMStatus{}, err
			}
		}
		p, err := c.Request(sit.SIMStatus, nil, 8*time.Second)
		if err == nil {
			st, perr := ParseSIMStatus(p)
			if perr == nil && (st.Card == CardAbsent || st.Card == CardPresent) {
				return c, st, nil
			}
			err = perr
			if err == nil {
				err = fmt.Errorf("card state %d", st.Card)
			}
		}
		last = err
		select {
		case <-c.Done():
			last = c.Err()
			c.Close()
			c = nil
		default:
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			last = ctx.Err()
		case <-time.After(time.Second):
			continue
		}
		break
	}
	if c == nil {
		var err error
		if c, err = open(s.Path); err != nil {
			return nil, SIMStatus{}, err
		}
	}
	return c, SIMStatus{}, last
}

func PickSlot(ctx context.Context, slots []Slot, open Opener, wait time.Duration, logf func(string, ...any)) (*sit.Client, Slot, error) {
	var fallback *sit.Client
	var fallbackSlot Slot
	for _, s := range slots {
		if s.Path == "" {
			continue
		}
		c, st, err := probe(ctx, s, open, wait)
		if c == nil {
			logf("slot %s (%s): %v", s.Name, s.Path, err)
			continue
		}
		switch {
		case err != nil:
			logf("slot %s: no card state: %v", s.Name, err)
		case st.Card == CardPresent:
			logf("slot %s: card present", s.Name)
			if fallback != nil {
				fallback.Close()
			}
			return c, s, nil
		default:
			logf("slot %s: no card", s.Name)
		}
		if fallback == nil {
			fallback, fallbackSlot = c, s
		} else {
			c.Close()
		}
		if ctx.Err() != nil {
			break
		}
	}
	if fallback == nil {
		return nil, Slot{}, errors.New("no SIM channel could be opened")
	}
	return fallback, fallbackSlot, nil
}
