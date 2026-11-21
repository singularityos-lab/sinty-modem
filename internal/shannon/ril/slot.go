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

func cardState(ctx context.Context, c *sit.Client, wait time.Duration) (SIMStatus, error) {
	deadline := time.Now().Add(wait)
	var last error
	for {
		p, err := c.Request(sit.SIMStatus, nil, 8*time.Second)
		if err == nil {
			st, perr := ParseSIMStatus(p)
			if perr == nil && (st.Card == CardAbsent || st.Card == CardPresent) {
				return st, nil
			}
			err = perr
			if err == nil {
				err = fmt.Errorf("card state %d", st.Card)
			}
		}
		last = err
		if time.Now().After(deadline) {
			return SIMStatus{}, last
		}
		select {
		case <-ctx.Done():
			return SIMStatus{}, ctx.Err()
		case <-c.Done():
			return SIMStatus{}, errors.New("SIT channel closed")
		case <-time.After(time.Second):
		}
	}
}

func PickSlot(ctx context.Context, slots []Slot, open Opener, wait time.Duration, logf func(string, ...any)) (*sit.Client, Slot, error) {
	var fallback *sit.Client
	var fallbackSlot Slot
	for _, s := range slots {
		if s.Path == "" {
			continue
		}
		c, err := open(s.Path)
		if err != nil {
			logf("slot %s (%s): %v", s.Name, s.Path, err)
			continue
		}
		st, err := cardState(ctx, c, wait)
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
