package ril

import (
	"encoding/binary"
	"errors"
	"strings"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

const (
	networkRecord = 14
	searchTimeout = 3 * time.Minute
)

type Network struct {
	PLMN   string
	Tech   uint32
	Status uint32
}

func (r *RIL) Networks() ([]Network, error) {
	p, err := r.c.Request(sit.QueryNetworks, nil, searchTimeout)
	if err != nil {
		return nil, err
	}
	return decodeNetworks(p)
}

func (r *RIL) SelectAutomatic() error {
	_, err := r.c.Request(sit.SelectNetworkAuto, nil, time.Minute)
	return err
}

func (r *RIL) SelectNetwork(n Network) error {
	p, err := selectionPayload(n)
	if err != nil {
		return err
	}
	_, err = r.c.Request(sit.SelectNetworkManual, p, time.Minute)
	return err
}

func decodeNetworks(p []byte) ([]Network, error) {
	if len(p) < 4 {
		return nil, errors.New("unexpected network search layout")
	}
	count := binary.LittleEndian.Uint32(p)
	if uint64(count)*networkRecord > uint64(len(p)-4) {
		return nil, errors.New("unexpected network search layout")
	}
	var list []Network
	for i := 0; i < int(count); i++ {
		rec := p[4+i*networkRecord:]
		plmn := strings.TrimRight(string(rec[4:10]), "#\x00")
		if !validPLMN(plmn) {
			continue
		}
		list = append(list, Network{
			PLMN:   plmn,
			Tech:   binary.LittleEndian.Uint32(rec),
			Status: binary.LittleEndian.Uint32(rec[10:]),
		})
	}
	return list, nil
}

func selectionPayload(n Network) ([]byte, error) {
	if !validPLMN(n.PLMN) {
		return nil, errors.New("a network code has 5 or 6 digits")
	}
	p := make([]byte, 10)
	binary.LittleEndian.PutUint32(p, n.Tech)
	p[9] = '#'
	copy(p[4:], n.PLMN)
	return p, nil
}

func validPLMN(s string) bool {
	return (len(s) == 5 || len(s) == 6) && strings.Trim(s, "0123456789") == ""
}
