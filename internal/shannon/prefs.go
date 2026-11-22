package shannon

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"

	"github.com/singularityos-lab/sinty-modem/internal/phone"
	"github.com/singularityos-lab/sinty-modem/internal/shannon/ril"
)

const maxAPN = 100

var validAPN = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

type prefs struct {
	Data    bool   `json:"data"`
	Roaming bool   `json:"roaming"`
	APN     string `json:"apn"`
	LTEOnly bool   `json:"lte_only"`
}

func loadPrefs(path string) prefs {
	p := prefs{Data: true, Roaming: true}
	b, err := os.ReadFile(path)
	if err != nil {
		return p
	}
	if err := json.Unmarshal(b, &p); err != nil {
		log.Printf("modem: network settings unreadable: %v", err)
		return prefs{Data: true, Roaming: true}
	}
	return p
}

func savePrefs(path string, p prefs) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".new", b, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".new", path)
}

func (s *Service) editPrefs(edit func(*prefs)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.prefs
	edit(&p)
	if err := savePrefs(s.settings.PrefsFile, p); err != nil {
		return fmt.Errorf("save the network settings: %w", err)
	}
	s.prefs = p
	return nil
}

func (s *Service) NetworkSettings() (data, roaming bool, apn string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prefs.Data, s.prefs.Roaming, s.prefs.APN
}

func (s *Service) IMEI() (string, error) { return s.ril.IMEI() }

func (s *Service) DataUsage() (rx, tx uint64) {
	if s.settings.Usage == nil {
		return 0, 0
	}
	return s.settings.Usage()
}

func (s *Service) SetData(on bool) error {
	if err := s.editPrefs(func(p *prefs) { p.Data = on }); err != nil {
		return err
	}
	return s.syncData()
}

func (s *Service) SetRoaming(on bool) error {
	if err := s.editPrefs(func(p *prefs) { p.Roaming = on }); err != nil {
		return err
	}
	return s.syncData()
}

func (s *Service) SetLTEOnly(on bool) error {
	if err := s.idle(); err != nil {
		return err
	}
	if err := s.ril.SetLTEOnly(on); err != nil {
		return err
	}
	if err := s.editPrefs(func(p *prefs) { p.LTEOnly = on }); err != nil {
		return err
	}
	s.update(func(x *phone.Status) { x.LTEOnly = on })
	return nil
}

func (s *Service) SetAPN(name string) error {
	if name != "" && (len(name) > maxAPN || !validAPN.MatchString(name)) {
		return phone.ErrBadAPN
	}
	if err := s.idle(); err != nil {
		return err
	}
	if err := s.editPrefs(func(p *prefs) { p.APN = name }); err != nil {
		return err
	}
	s.reload()
	return nil
}

func (s *Service) customAPN() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prefs.APN != "" {
		return s.prefs.APN
	}
	return s.settings.APN
}

func (s *Service) internetAPN() string {
	if name := s.customAPN(); name != "" {
		return name
	}
	if s.carrier != nil {
		return s.carrier.Internet
	}
	return ""
}

func (s *Service) dataWanted() bool {
	roaming := s.Status().Registration == "roaming"
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prefs.Data && (s.prefs.Roaming || !roaming)
}

func (s *Service) syncData() error {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	st := s.Status()
	if st.Stage != phone.StageReady || !st.Radio {
		return nil
	}
	s.mu.Lock()
	up := s.internet
	s.mu.Unlock()
	switch want := s.dataWanted(); {
	case want && up == nil:
		if name := s.internetAPN(); name != "" {
			return s.startInternet(name, s.settings.IPType)
		}
	case !want && up != nil:
		return s.stopInternet(up)
	}
	return nil
}

func (s *Service) resyncData() {
	if err := s.syncData(); err != nil {
		log.Printf("modem: data after a change: %v", err)
	}
}

func (s *Service) restoreData() (bool, error) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	st := s.Status()
	if st.Stage == phone.StageOff || !st.Radio || st.Data {
		return true, nil
	}
	internet := s.internetAPN()
	if internet == "" || !s.dataWanted() {
		return true, nil
	}
	if err := s.startInternet(internet, s.settings.IPType); err != nil {
		return false, err
	}
	log.Printf("modem: internet bearer set up again")
	return true, nil
}

func (s *Service) stopInternet(b ril.Bearer) error {
	s.mu.Lock()
	s.internet = nil
	s.mu.Unlock()
	if s.net != nil {
		s.net.Down(fmt.Sprintf("rmnet%d", ril.InternetCID-1))
	}
	s.update(func(x *phone.Status) { x.Data = false })
	return s.ril.Deactivate(ril.InternetCID)
}
