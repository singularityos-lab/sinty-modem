package shannon

import (
	"errors"

	"github.com/singularityos-lab/sinty-modem/internal/phone"
)

var errSearchFirst = errors.New("search for networks before choosing one")

func (s *Service) SearchNetworks() ([]phone.Network, error) {
	if err := s.idle(); err != nil {
		return nil, err
	}
	found, err := s.ril.Networks()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.found = found
	s.mu.Unlock()
	var list []phone.Network
	seen := map[string]bool{}
	for _, n := range found {
		if seen[n.PLMN] {
			continue
		}
		seen[n.PLMN] = true
		status := n.Status
		if status > 3 {
			status = 0
		}
		list = append(list, phone.Network{Code: n.PLMN, Status: status})
	}
	return list, nil
}

func (s *Service) SelectNetwork(code string) error {
	if err := s.idle(); err != nil {
		return err
	}
	if code == "" {
		return s.ril.SelectAutomatic()
	}
	s.mu.Lock()
	found := s.found
	s.mu.Unlock()
	for _, n := range found {
		if n.PLMN == code {
			return s.ril.SelectNetwork(n)
		}
	}
	return errSearchFirst
}
