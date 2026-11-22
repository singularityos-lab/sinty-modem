package shannon

import (
	"log"
	"time"

	"github.com/damonto/euicc-go/driver"

	"github.com/singularityos-lab/sinty-modem/internal/esim"
)

const reloadDelay = time.Second

type Profiles struct {
	*esim.Manager
	s *Service
}

func (s *Service) ESIM() *Profiles {
	open := func() driver.SmartCardChannel { return s.ril.EUICC() }
	return &Profiles{esim.New(open, s.ril.IMEI, s.reload), s}
}

func (p *Profiles) Enable(iccid string) error {
	if err := p.s.idle(); err != nil {
		return err
	}
	return p.Manager.Enable(iccid)
}

func (p *Profiles) Disable(iccid string) error {
	if err := p.s.idle(); err != nil {
		return err
	}
	return p.Manager.Disable(iccid)
}

func (s *Service) reload() {
	time.AfterFunc(reloadDelay, func() {
		if err := s.Restart(); err != nil {
			log.Printf("modem: the SIM reload waits for a restart: %v", err)
		}
	})
}
