package esim

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/damonto/euicc-go/driver"
	"github.com/damonto/euicc-go/lpa"
	sgp22 "github.com/damonto/euicc-go/v2"
)

const (
	httpTimeout    = 30 * time.Second
	installTimeout = 5 * time.Minute
	maxNickname    = 64
)

var (
	ErrNoEUICC      = errors.New("the eSIM chip did not answer")
	ErrNoProfile    = errors.New("this eSIM profile is not on the phone")
	ErrActive       = errors.New("turn the eSIM off before deleting it")
	ErrBadCode      = errors.New("this is not an eSIM activation code")
	ErrConfirmation = errors.New("this eSIM needs the confirmation code from the carrier")
	ErrName         = errors.New("the name is too long")
)

type Profile struct {
	ICCID    string
	Name     string
	Nickname string
	Provider string
	Enabled  bool
}

type Manager struct {
	open    func() driver.SmartCardChannel
	imei    func() (string, error)
	changed func()

	mu sync.Mutex
}

func New(open func() driver.SmartCardChannel, imei func() (string, error), changed func()) *Manager {
	return &Manager{open: open, imei: imei, changed: changed}
}

func (m *Manager) session(fn func(*lpa.Client) error) error {
	c, err := lpa.New(&lpa.Options{Channel: m.open(), Logger: slog.New(slog.DiscardHandler), Timeout: httpTimeout})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoEUICC, err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			log.Printf("esim: channel close: %v", err)
		}
	}()
	return fn(c)
}

func (m *Manager) EID() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var eid string
	err := m.session(func(c *lpa.Client) error {
		b, err := c.EID()
		eid = fmt.Sprintf("%X", b)
		return err
	})
	return eid, err
}

func (m *Manager) Profiles() ([]Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Profile
	err := m.session(func(c *lpa.Client) error {
		list, err := c.ListProfile(nil, nil)
		if err != nil {
			return err
		}
		for _, p := range list {
			out = append(out, Profile{
				ICCID:    p.ICCID.String(),
				Name:     p.ProfileName,
				Nickname: p.ProfileNickname,
				Provider: p.ServiceProviderName,
				Enabled:  p.ProfileState == sgp22.ProfileEnabled,
			})
		}
		return nil
	})
	return out, err
}

func find(c *lpa.Client, iccid string) (*sgp22.ProfileInfo, error) {
	list, err := c.ListProfile(nil, nil)
	if err != nil {
		return nil, err
	}
	for _, p := range list {
		if p.ICCID.String() == iccid {
			return p, nil
		}
	}
	return nil, ErrNoProfile
}

func (m *Manager) Enable(iccid string) error  { return m.switchProfile(iccid, true) }
func (m *Manager) Disable(iccid string) error { return m.switchProfile(iccid, false) }

func (m *Manager) switchProfile(iccid string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var switched bool
	err := m.session(func(c *lpa.Client) error {
		p, err := find(c, iccid)
		if err != nil {
			return err
		}
		if (p.ProfileState == sgp22.ProfileEnabled) == on {
			return nil
		}
		if on {
			err = c.EnableProfile(p.ICCID, true)
		} else {
			err = c.DisableProfile(p.ICCID, true)
		}
		switched = err == nil
		return err
	})
	if switched {
		m.notify()
		m.changed()
	}
	return err
}

func (m *Manager) Rename(iccid, name string) error {
	if len(name) > maxNickname {
		return ErrName
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.session(func(c *lpa.Client) error {
		p, err := find(c, iccid)
		if err != nil {
			return err
		}
		return c.SetNickname(p.ICCID, name)
	})
}

func (m *Manager) Delete(iccid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	err := m.session(func(c *lpa.Client) error {
		p, err := find(c, iccid)
		if err != nil {
			return err
		}
		if p.ProfileState == sgp22.ProfileEnabled {
			return ErrActive
		}
		return c.DeleteProfile(p.ICCID)
	})
	if err == nil {
		m.notify()
	}
	return err
}

func activationCode(text, confirmation string) (*lpa.ActivationCode, error) {
	code := strings.TrimPrefix(strings.TrimSpace(text), "LPA:")
	parts := strings.Split(code, "$")
	if len(parts) < 2 || parts[0] != "1" || parts[1] == "" || strings.ContainsAny(parts[1], "/@?#\\ ") {
		return nil, ErrBadCode
	}
	u, err := url.Parse("https://" + parts[1])
	if err != nil || u.Hostname() == "" {
		return nil, ErrBadCode
	}
	ac := &lpa.ActivationCode{SMDP: u, ConfirmationCode: strings.TrimSpace(confirmation)}
	if len(parts) > 2 {
		ac.MatchingID = parts[2]
	}
	if len(parts) > 3 {
		ac.OID = parts[3]
	}
	if len(parts) > 4 && parts[4] == "1" && ac.ConfirmationCode == "" {
		return nil, ErrConfirmation
	}
	return ac, nil
}

func (m *Manager) Install(text, confirmation string) error {
	ac, err := activationCode(text, confirmation)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ac.IMEI, err = m.imei(); err != nil {
		return fmt.Errorf("read the IMEI: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	err = m.session(func(c *lpa.Client) error {
		var asked bool
		res, err := c.DownloadProfile(ctx, ac, &lpa.DownloadOptions{
			OnEnterConfirmationCode: func() string {
				asked = true
				return ""
			},
		})
		switch {
		case asked:
			return ErrConfirmation
		case err != nil:
			return err
		case res == nil:
			return context.DeadlineExceeded
		}
		return nil
	})
	if err != nil {
		return err
	}
	m.notify()
	return nil
}

func (m *Manager) SendNotifications() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.session(sendPending)
}

func (m *Manager) notify() {
	if err := m.session(sendPending); err != nil {
		log.Printf("esim: carrier notifications left for later: %v", err)
	}
}

func sendPending(c *lpa.Client) error {
	list, err := c.ListNotification()
	if err != nil {
		return err
	}
	for _, n := range list {
		pending, err := c.RetrieveNotificationList(n.SequenceNumber)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			continue
		}
		for _, p := range pending {
			if err := c.HandleNotification(p); err != nil {
				return err
			}
		}
		if err := c.RemoveNotificationFromList(n.SequenceNumber); err != nil {
			return err
		}
	}
	return nil
}
