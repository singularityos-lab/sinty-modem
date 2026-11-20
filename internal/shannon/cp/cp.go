package cp

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	BootDevice = "/dev/umts_boot0"
	allowPower = "/sys/module/cpif/parameters/allow_poweroff"

	ioctlPowerOff    = 0x6f20
	ioctlStatus      = 0x6f27
	ioctlBootLog     = 0x6f47
	ioctlCrashReason = 0x82046f55
)

type State int

const (
	Offline State = iota
	CrashReset
	CrashExit
	Booting
	Online
	NVRebuilding
	LoaderDone
	SIMAttach
	SIMDetach
	CrashWatchdog
)

var stateNames = []string{"OFFLINE", "CRASH_RESET", "CRASH_EXIT", "BOOTING", "ONLINE",
	"NV_REBUILDING", "LOADER_DONE", "SIM_ATTACH", "SIM_DETACH", "CRASH_WATCHDOG"}

func (s State) String() string {
	if int(s) >= 0 && int(s) < len(stateNames) {
		return stateNames[s]
	}
	return fmt.Sprintf("STATE_%d", int(s))
}

func ioctl(cmd uintptr, arg uintptr) (uintptr, error) {
	f, err := os.OpenFile(BootDevice, os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), cmd, arg)
	if errno != 0 {
		return 0, errno
	}
	return r, nil
}

func Status() (State, error) {
	r, err := ioctl(ioctlStatus, 0)
	if errors.Is(err, os.ErrNotExist) {
		return Offline, nil
	}
	return State(r), err
}

func WaitFor(want State, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s, err := Status(); err == nil && s == want {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func PowerOff() error {
	s, err := Status()
	if err != nil {
		return err
	}
	if s == Offline {
		return nil
	}
	prev, perr := os.ReadFile(allowPower)
	if perr == nil {
		os.WriteFile(allowPower, []byte("Y"), 0)
		defer os.WriteFile(allowPower, bytes.TrimSpace(prev), 0)
	}
	if _, err := ioctl(ioctlPowerOff, 0); err != nil {
		return err
	}
	if !WaitFor(Offline, 15*time.Second) {
		return errors.New("cp: the modem did not reach OFFLINE")
	}
	return nil
}

func CrashReason() (uint32, string, error) {
	buf := make([]byte, 4+512)
	if _, err := ioctl(ioctlCrashReason, uintptr(unsafe.Pointer(&buf[0]))); err != nil {
		return 0, "", err
	}
	text := buf[4:]
	if i := bytes.IndexByte(text, 0); i >= 0 {
		text = text[:i]
	}
	return binary.LittleEndian.Uint32(buf), string(text), nil
}

func RequestBootLog() error {
	_, err := ioctl(ioctlBootLog, 0)
	return err
}

func BasebandVersion(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	toc := make([]byte, 512)
	if _, err := io.ReadFull(f, toc); err != nil {
		return "", err
	}
	if !bytes.Equal(toc[:4], []byte("TOC\x00")) {
		return "", errors.New("cp: no TOC at the start of the modem image")
	}
	last := int(binary.LittleEndian.Uint32(toc[28:])) + 1
	if last < 1 || last > 15 {
		return "", errors.New("cp: unexpected TOC entry count")
	}
	for i := last; i >= 1; i-- {
		e := toc[i*32 : i*32+32]
		name := string(bytes.TrimRight(e[:12], "\x00"))
		if j := strings.IndexByte(name, 0); j >= 0 {
			name = name[:j]
		}
		if name != "GVERSION" && name != "INFO" {
			continue
		}
		off := binary.LittleEndian.Uint32(e[12:])
		size := binary.LittleEndian.Uint32(e[20:])
		if off == 0 || size == 0 {
			return "", fmt.Errorf("cp: empty %s entry", name)
		}
		skip := int64(0)
		if name == "INFO" {
			skip = 20
		}
		n := min(int(size), 31)
		b := make([]byte, n)
		if _, err := f.ReadAt(b, int64(off)+skip); err != nil && err != io.EOF {
			return "", err
		}
		if j := bytes.IndexByte(b, 0); j >= 0 {
			b = b[:j]
		}
		if len(b) == 0 {
			return "", fmt.Errorf("cp: empty %s section", name)
		}
		for _, c := range b {
			if c < 0x20 || c > 0x7e {
				return "", fmt.Errorf("cp: unexpected %s section", name)
			}
		}
		return string(b), nil
	}
	return "", errors.New("cp: no GVERSION or INFO entry in the TOC")
}

var nvSalt = []byte("Samsung_SIT_RIL")

func NVCheck(efs, efsBackup string) error {
	var bad []string
	for _, f := range []string{
		filepath.Join(efs, "nv_normal.bin"),
		filepath.Join(efs, "nv_protected.bin"),
		filepath.Join(efsBackup, "nv_protected.bak"),
	} {
		data, err := os.ReadFile(f)
		if err != nil {
			bad = append(bad, err.Error())
			continue
		}
		stored, err := os.ReadFile(f + ".md5")
		if err != nil {
			bad = append(bad, err.Error())
			continue
		}
		sum := md5.Sum(append(data, nvSalt...))
		if hex.EncodeToString(sum[:]) != string(bytes.TrimSpace(stored)) {
			bad = append(bad, filepath.Base(f)+": the stock checksum does not match")
		}
	}
	if len(bad) > 0 {
		return errors.New("cp: " + strings.Join(bad, "; "))
	}
	return nil
}
