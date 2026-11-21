package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/singularityos-lab/sinty-modem/internal/audio"
	"github.com/singularityos-lab/sinty-modem/internal/device"
	"github.com/singularityos-lab/sinty-modem/internal/mm"
	"github.com/singularityos-lab/sinty-modem/internal/netcfg"
	"github.com/singularityos-lab/sinty-modem/internal/shannon"
	"github.com/singularityos-lab/sinty-modem/internal/shannon/cp"
	"github.com/singularityos-lab/sinty-modem/internal/shannon/ril"
	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
	"github.com/singularityos-lab/sinty-modem/internal/shannon/vendor"
	"github.com/singularityos-lab/sinty-modem/internal/store"
)

const (
	stateDir    = "/var/lib/sinty-modem"
	runDir      = "/run/sinty-modem"
	basebandOut = "/run/sinty/modem-baseband"
	maxAttempts = 3
)

func notify(state string) {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return
	}
	if addr[0] == '@' {
		addr = "\x00" + addr[1:]
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		log.Printf("notify: %v", err)
		return
	}
	defer c.Close()
	c.Write([]byte(state))
}

func modem() *device.Modem {
	m, err := device.FindModem()
	if err != nil {
		log.Fatalf("sinty-modemd: %v", err)
	}
	if m.Driver != "shannon" {
		log.Fatalf("sinty-modemd: unsupported modem driver %q", m.Driver)
	}
	return m
}

func runStack() {
	m := modem()
	latch := vendor.Latch{Path: filepath.Join(stateDir, "bringup-attempts")}
	if n := latch.Attempts(); n >= maxAttempts {
		log.Fatalf("stack: %d bring-up attempts did not reach ONLINE; remove %s to try again", n, latch.Path)
	}
	if _, err := latch.Arm(); err != nil {
		log.Fatalf("stack: %v", err)
	}
	if err := vendor.LoadModules(m.LinkModules, m.Modules, m.ASPM); err != nil {
		log.Fatalf("stack: %v", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		log.Fatalf("stack: %v", err)
	}
	self, _ := os.Executable()
	cmd := exec.Command(self, "stack-ns")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{w}
	cmd.SysProcAttr = &syscall.SysProcAttr{Unshareflags: syscall.CLONE_NEWNS, Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		log.Fatalf("stack: %v", err)
	}
	w.Close()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sigs
		notify("STOPPING=1")
		cmd.Process.Signal(s)
	}()
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if sc.Text() == "ready" {
				latch.Clear()
				notify("READY=1\nSTATUS=modem online")
			}
		}
	}()
	err = cmd.Wait()
	if err != nil {
		log.Fatalf("stack: %v", err)
	}
}

func runStackNS() {
	m := modem()
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		log.Fatalf("stack: private mount namespace: %v", err)
	}
	ready := os.NewFile(3, "ready")
	st := vendor.New(runDir, stateDir, m.ModemType)
	if err := st.Setup(); err != nil {
		log.Fatalf("stack: %v", err)
	}
	if err := st.Start(90 * time.Second); err != nil {
		st.Stop()
		log.Fatalf("stack: %v", err)
	}
	if st.Baseband != "" {
		os.MkdirAll(filepath.Dir(basebandOut), 0o755)
		os.WriteFile(basebandOut, []byte(st.Baseband+"\n"), 0o644)
	}
	log.Printf("stack: modem online")
	fmt.Fprintln(ready, "ready")
	ready.Close()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	<-sigs
	log.Printf("stack: stopping")
	if err := st.Stop(); err != nil {
		log.Printf("stack: %v", err)
	}
}

func readModel() string {
	b, err := os.ReadFile("/proc/device-tree/model")
	if err != nil {
		return "Phone"
	}
	return strings.TrimRight(string(b), "\x00\n ")
}

func runService() {
	m := modem()
	if !cp.WaitFor(cp.Online, 3*time.Minute) {
		st, _ := cp.Status()
		log.Fatalf("sinty-modemd: the modem is %s, not ONLINE", st)
	}
	slots := []ril.Slot{{Name: "sim", Path: m.IPC}, {Name: "esim", Path: m.ESIMIPC}}
	c, slot, err := ril.PickSlot(context.Background(), slots, sit.Open, 30*time.Second, func(f string, a ...any) { log.Printf("sinty-modemd: "+f, a...) })
	if err != nil {
		log.Fatalf("sinty-modemd: %v", err)
	}
	defer c.Close()
	log.Printf("sinty-modemd: using the %s slot on %s", slot.Name, slot.Path)
	msgs, err := store.Open(filepath.Join(stateDir, "messages"))
	if err != nil {
		log.Fatalf("sinty-modemd: %v", err)
	}
	var voice shannon.Audio
	if m.AudioCard != "" {
		if n, err := audio.FindCard(m.AudioCard); err == nil {
			voice = &audio.AoC{Card: n}
		} else {
			log.Printf("sinty-modemd: no call audio: %v", err)
		}
	}
	network := netcfg.New(runDir)
	model := readModel()
	maker := "Google"
	if strings.HasPrefix(model, "Google ") {
		model = strings.TrimPrefix(model, "Google ")
	}
	svc := shannon.New(ril.New(c, m.Router), msgs, shannon.Settings{
		Identity: ril.Identity{Manufacturer: maker, Model: model, OS: "Sinty OS"},
	}, voice, network)
	if b, err := os.ReadFile(basebandOut); err == nil {
		svc.SetFirmware(strings.TrimSpace(string(b)))
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		log.Fatalf("sinty-modemd: system bus: %v", err)
	}
	if _, err := mm.Export(conn, svc); err != nil {
		log.Fatalf("sinty-modemd: %v", err)
	}
	log.Printf("sinty-modemd: serving %s", mm.Name)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		select {
		case <-c.Done():
			stop()
		case <-ctx.Done():
		}
	}()
	svc.Run(ctx)
	network.Down("")
	if errors.Is(ctx.Err(), context.Canceled) {
		select {
		case <-c.Done():
			os.Exit(1)
		default:
		}
	}
}

func main() {
	log.SetFlags(0)
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "stack":
		runStack()
	case "stack-ns":
		runStackNS()
	case "", "serve":
		runService()
	default:
		fmt.Fprintln(os.Stderr, "usage: sinty-modemd [serve|stack]")
		os.Exit(2)
	}
}
