package audio

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	protectionStatus = "/run/omamobile/speaker-protection"
	earpieceCommand  = "/usr/bin/omamobile-speaker-protection"
	voicePlayback    = 4
	voiceCapture     = 11
)

var (
	valuesRe = regexp.MustCompile(`(?m)^\s*: values=([0-9,\-]+|on|off)$`)
	cardRe   = regexp.MustCompile(`^\s*(\d+)\s+\[[^\]]*\]:\s*.*? - (\S+)\s*$`)
)

type ceiling struct {
	name string
	max  int
}

var mediaCeilings = []ceiling{{"Top Analog PCM Volume", 6}, {"Bottom Analog PCM Volume", 10}, {"Top Digital PCM Volume", 737}, {"Bottom Digital PCM Volume", 737}}
var earpieceCeilings = []ceiling{{"Top Analog PCM Volume", 6}, {"Top Digital PCM Volume", 737}, {"Bottom Digital PCM Volume", 0}}

type AoC struct {
	Card     int
	MicBoost int

	mu       sync.Mutex
	saved    [][2]string
	pcms     []*PCM
	earpiece bool
	running  bool
}

func FindCard(id string) (int, error) {
	b, err := os.ReadFile("/proc/asound/cards")
	if err != nil {
		return -1, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		m := cardRe.FindStringSubmatch(line)
		if m != nil && m[2] == id {
			return strconv.Atoi(m[1])
		}
	}
	return -1, fmt.Errorf("no sound card %s", id)
}

func (a *AoC) mixer(args ...string) (string, error) {
	cmd := exec.Command("amixer", append([]string{"-c", strconv.Itoa(a.Card)}, args...)...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		<-done
		return "", errors.New("amixer timed out")
	}
	if err != nil {
		return "", fmt.Errorf("amixer %s: %v", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (a *AoC) get(name string) (string, error) {
	out, err := a.mixer("cget", "name="+name)
	if err != nil {
		return "", err
	}
	m := valuesRe.FindStringSubmatch(out)
	if m == nil {
		return "", errors.New("mixer state unavailable: " + name)
	}
	return m[1], nil
}

func (a *AoC) set(name, value string) error {
	_, err := a.mixer("cset", "name="+name, value)
	return err
}

func (a *AoC) earpieceMode(on bool) error {
	mode := "off"
	if on {
		mode = "on"
	}
	out, err := exec.Command("/bin/sh", "-c", `set -a; [ -r /etc/omamobile/speaker.conf ] && . /etc/omamobile/speaker.conf; set +a; exec "$0" earpiece "$1"`, earpieceCommand, mode).CombinedOutput()
	if err != nil {
		return fmt.Errorf("earpiece mode %s: %v: %s", mode, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (a *AoC) checkLevels() error {
	b, _ := os.ReadFile(protectionStatus)
	status := strings.TrimSpace(string(b))
	earpiece := strings.HasPrefix(status, "ok: stage ") && strings.Contains(status, ", mode earpiece:")
	media := strings.HasPrefix(status, "ok: stage media,") || strings.HasPrefix(status, "ok: stage c8,")
	if !earpiece && !media {
		return fmt.Errorf("call audio refused: the speaker protection is not running (%s)", status)
	}
	list := mediaCeilings
	if earpiece {
		list = earpieceCeilings
	}
	for _, c := range list {
		v, err := a.get(c.name)
		if err != nil {
			return err
		}
		for _, f := range strings.Split(v, ",") {
			n, err := strconv.Atoi(f)
			if err != nil || n > c.max {
				return fmt.Errorf("call audio refused: %s is above %d", c.name, c.max)
			}
		}
	}
	return nil
}

func (a *AoC) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return nil
	}
	if _, err := os.Stat(earpieceCommand); err == nil {
		if err := a.earpieceMode(true); err != nil {
			return err
		}
		a.earpiece = true
	}
	if err := a.checkLevels(); err != nil {
		a.restore()
		return err
	}
	route := [][2]string{
		{"Voice Call Audio Enable", "1"}, {"Voice Call Mic Source", "1"},
		{"Voice Call Mic Mute", "0"}, {"Incall Mic Mute", "0"},
		{"Incall Sink Mute", "0"}, {"AoC Modem Downlink ASRC Mode", "1"},
		{"MIC HW Gain At High Power Mode (cB)", strconv.Itoa(130 + 10*a.MicBoost)},
		{"MIC Record Soft Gain (dB)", "0"},
		{"TDM_0_RX Mixer EP5", "1"}, {"EP4 TX Mixer INTERNAL_MIC_TX", "1"},
	}
	for _, kv := range route {
		old, err := a.get(kv[0])
		if err != nil {
			a.restore()
			return err
		}
		a.saved = append(a.saved, [2]string{kv[0], old})
		if err := a.set(kv[0], kv[1]); err != nil {
			a.restore()
			return err
		}
	}
	for _, d := range []struct {
		dev     int
		capture bool
	}{{voiceCapture, true}, {voicePlayback, false}} {
		p, err := OpenPCM(a.Card, d.dev, d.capture, 2, 48000, 960, 4)
		if err != nil {
			a.restore()
			return err
		}
		a.pcms = append(a.pcms, p)
	}
	a.running = true
	log.Printf("audio: AoC voice route up")
	return nil
}

func (a *AoC) restore() []string {
	var failed []string
	for i := len(a.pcms) - 1; i >= 0; i-- {
		if err := a.pcms[i].Close(); err != nil {
			failed = append(failed, "PCM close")
		}
	}
	a.pcms = nil
	for i := len(a.saved) - 1; i >= 0; i-- {
		if err := a.set(a.saved[i][0], a.saved[i][1]); err != nil {
			failed = append(failed, a.saved[i][0])
		}
	}
	a.saved = nil
	if a.earpiece {
		if err := a.earpieceMode(false); err != nil {
			failed = append(failed, "earpiece mode off")
		}
		a.earpiece = false
	}
	a.running = false
	return failed
}

func (a *AoC) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running && a.saved == nil && !a.earpiece {
		return nil
	}
	if failed := a.restore(); len(failed) > 0 {
		return errors.New("voice route cleanup failed: " + strings.Join(failed, ", "))
	}
	log.Printf("audio: AoC voice route restored")
	return nil
}

func (a *AoC) Mute(on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running {
		return errors.New("no call audio is running")
	}
	v := "0"
	if on {
		v = "1"
	}
	return a.set("Incall Mic Mute", v)
}
