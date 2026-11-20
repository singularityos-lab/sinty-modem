package vendor

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/android/apex"
	"github.com/singularityos-lab/sinty-modem/internal/android/bootconfig"
	"github.com/singularityos-lab/sinty-modem/internal/android/loop"
	"github.com/singularityos-lab/sinty-modem/internal/android/lp"
	"github.com/singularityos-lab/sinty-modem/internal/android/props"
	"github.com/singularityos-lab/sinty-modem/internal/shannon/cp"
)

var nvParts = []string{"efs", "efs_backup", "modem_userdata", "persist"}

type Stack struct {
	Run       string
	State     string
	ModemType string
	Baseband  string

	slot  string
	root  string
	nv    string
	area  *props.Mapped
	svc   *props.Service
	mu    sync.Mutex
	procs map[string]*exec.Cmd
}

func New(run, state, modemType string) *Stack {
	return &Stack{Run: run, State: state, ModemType: modemType, root: filepath.Join(run, "root"), nv: filepath.Join(run, "nv"), procs: map[string]*exec.Cmd{}}
}

func mount(src, dst, fstype string, flags uintptr, data string) error {
	if err := syscall.Mount(src, dst, fstype, flags, data); err != nil {
		return fmt.Errorf("mount %s on %s: %w", src, dst, err)
	}
	return nil
}

func bind(src, dst string, ro bool) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if st.IsDir() {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		f.Close()
	}
	if err := mount(src, dst, "", syscall.MS_BIND, ""); err != nil {
		return err
	}
	if ro {
		return mount("", dst, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY, "")
	}
	return nil
}

func mountImage(dev, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	err := syscall.Mount(dev, dir, "ext4", syscall.MS_RDONLY|syscall.MS_NODEV|syscall.MS_NOSUID, "noload")
	if err == nil {
		return nil
	}
	if err2 := syscall.Mount(dev, dir, "erofs", syscall.MS_RDONLY|syscall.MS_NODEV|syscall.MS_NOSUID, ""); err2 == nil {
		return nil
	}
	return fmt.Errorf("mount %s: %w", dev, err)
}

func (s *Stack) Check() error {
	var problems []string
	for _, n := range []string{"umts_boot0", "umts_rfs0", "umts_ipc0"} {
		if st, err := os.Stat("/dev/" + n); err != nil || st.Mode()&os.ModeCharDevice == 0 {
			problems = append(problems, "no /dev/"+n)
		}
	}
	for _, p := range append([]string{"super", "modem_a", "modem_b"}, nvParts...) {
		b, err := FindPartition(p)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if p != "super" && p != "persist" && !b.ReadOnly {
			problems = append(problems, p+" is writable")
		}
		if p != "super" && b.Mounted() {
			problems = append(problems, p+" is mounted")
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func (s *Stack) Setup() error {
	if err := s.Check(); err != nil {
		return err
	}
	cfg, err := bootconfig.Read()
	if err != nil {
		return err
	}
	boot := bootconfig.AndroidProps(cfg)
	s.slot = boot["ro.boot.slot_suffix"]
	if s.slot != "_a" && s.slot != "_b" {
		return fmt.Errorf("unexpected slot suffix %q", s.slot)
	}
	if boot["ro.boot.cdt_hwid"] == "" {
		return errors.New("the bootloader passed no cdt_hwid: the modem firmware hangs without it")
	}
	if err := os.MkdirAll(s.Run, 0o700); err != nil {
		return err
	}
	if err := mount("sinty-modem", s.Run, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=0700,size=900m"); err != nil {
		return err
	}
	nodes := filepath.Join(s.Run, "nodes")
	img := filepath.Join(s.Run, "img")

	super, err := FindPartition("super")
	if err != nil {
		return err
	}
	superNode, err := super.Node(nodes)
	if err != nil {
		return err
	}
	sf, err := os.Open(superNode)
	if err != nil {
		return err
	}
	slot := 0
	if s.slot == "_b" {
		slot = 1
	}
	meta, err := lp.ReadDevice(sf, slot)
	sf.Close()
	if err != nil {
		return err
	}
	for _, part := range []string{"system", "vendor"} {
		node, err := MapDynamic(meta, super, part+s.slot, "sinty-modem-"+part, nodes)
		if err != nil {
			return err
		}
		if err := mountImage(node, filepath.Join(img, part)); err != nil {
			return err
		}
	}
	apexPath := filepath.Join(img, "system", "system", "apex", "com.android.runtime.apex")
	af, off, size, err := apex.Open(apexPath)
	if err != nil {
		return err
	}
	ld, err := loop.Attach(af, off, size, true)
	af.Close()
	if err != nil {
		return err
	}
	err = mountImage(ld.Path, filepath.Join(img, "runtime"))
	ld.Close()
	if err != nil {
		return err
	}
	if err := s.buildRoot(img); err != nil {
		return err
	}
	if err := s.setupNV(nodes); err != nil {
		return err
	}
	if err := s.setupProps(img, boot); err != nil {
		return err
	}
	return s.setupDevices()
}

func (s *Stack) buildRoot(img string) error {
	r := s.root
	if err := os.MkdirAll(r, 0o755); err != nil {
		return err
	}
	if err := mount("sinty-modem-root", r, "tmpfs", syscall.MS_NOSUID, "mode=0755"); err != nil {
		return err
	}
	if err := bind(filepath.Join(img, "system", "system"), filepath.Join(r, "system"), true); err != nil {
		return err
	}
	if err := bind(filepath.Join(img, "vendor"), filepath.Join(r, "vendor"), true); err != nil {
		return err
	}
	if err := bind(filepath.Join(img, "runtime"), filepath.Join(r, "apex", "com.android.runtime"), true); err != nil {
		return err
	}
	for _, d := range []string{"proc", "sys", "dev", "data", "tmp", "linkerconfig", "mnt/vendor"} {
		if err := os.MkdirAll(filepath.Join(r, d), 0o755); err != nil {
			return err
		}
	}
	os.Symlink("/system/bin", filepath.Join(r, "bin"))
	os.Symlink("/system/etc", filepath.Join(r, "etc"))
	if err := mount("tmp", filepath.Join(r, "tmp"), "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=1777"); err != nil {
		return err
	}
	if err := mount("proc", filepath.Join(r, "proc"), "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
		return err
	}
	data := filepath.Join(s.State, "data")
	for _, d := range []string{"vendor/log/cbd", "vendor/log/rfsd", "vendor/radio", "vendor/ssrdump/coredump"} {
		if err := os.MkdirAll(filepath.Join(data, d), 0o700); err != nil {
			return err
		}
	}
	return bind(data, filepath.Join(r, "data"), false)
}

func (s *Stack) setupNV(nodes string) error {
	if err := os.MkdirAll(s.nv, 0o700); err != nil {
		return err
	}
	sums := map[string]string{}
	modem := "modem" + s.slot
	for _, p := range append([]string{modem}, nvParts...) {
		b, err := FindPartition(p)
		if err != nil {
			return err
		}
		node, err := b.Node(nodes)
		if err != nil {
			return err
		}
		sum, err := CopyDevice(node, filepath.Join(s.nv, p+".img"))
		if err != nil {
			return err
		}
		sums[p] = sum
	}
	if err := s.backup(sums); err != nil {
		return err
	}
	mnt := filepath.Join(s.root, "mnt", "vendor")
	byName := filepath.Join(s.root, "dev", "block", "by-name")
	if err := mount("dev", filepath.Join(s.root, "dev"), "tmpfs", syscall.MS_NOSUID, "mode=0755"); err != nil {
		return err
	}
	mf, err := os.Open(filepath.Join(s.nv, modem+".img"))
	if err != nil {
		return err
	}
	ml, err := loop.Attach(mf, 0, 0, true)
	mf.Close()
	if err != nil {
		return err
	}
	if err := bind(ml.Path, filepath.Join(byName, modem), true); err != nil {
		ml.Close()
		return err
	}
	if err := bind(ml.Path, filepath.Join(byName, "modem"), true); err != nil {
		ml.Close()
		return err
	}
	err = mountImage(ml.Path, filepath.Join(mnt, "modem_img"))
	ml.Close()
	if err != nil {
		return err
	}
	for _, p := range []string{"efs", "efs_backup", "modem_userdata"} {
		f, err := os.OpenFile(filepath.Join(s.nv, p+".img"), os.O_RDWR, 0)
		if err != nil {
			return err
		}
		l, err := loop.Attach(f, 0, 0, false)
		f.Close()
		if err != nil {
			return err
		}
		dir := filepath.Join(mnt, p)
		os.MkdirAll(dir, 0o755)
		err = mount(l.Path, dir, "f2fs", syscall.MS_NOATIME|syscall.MS_NOSUID|syscall.MS_NODEV, "")
		l.Close()
		if err != nil {
			return err
		}
	}
	if err := cp.NVCheck(filepath.Join(mnt, "efs"), filepath.Join(mnt, "efs_backup")); err != nil {
		return fmt.Errorf("the NV copies fail the stock checksum, rfsd would crash the modem: %w", err)
	}
	return s.setupPersist(mnt)
}

func (s *Stack) setupPersist(mnt string) error {
	f, err := os.Open(filepath.Join(s.nv, "persist.img"))
	if err != nil {
		return err
	}
	l, err := loop.Attach(f, 0, 0, true)
	f.Close()
	if err != nil {
		return err
	}
	ro := filepath.Join(s.nv, "persist-ro")
	os.MkdirAll(ro, 0o700)
	err = syscall.Mount(l.Path, ro, "f2fs", syscall.MS_RDONLY, "norecovery")
	if err != nil {
		err = syscall.Mount(l.Path, ro, "ext4", syscall.MS_RDONLY, "noload")
	}
	l.Close()
	if err != nil {
		return fmt.Errorf("mount persist copy: %w", err)
	}
	dst := filepath.Join(s.nv, "persist", "modem")
	os.MkdirAll(dst, 0o700)
	if _, err := os.Stat(filepath.Join(ro, "modem")); err == nil {
		if out, err := exec.Command("cp", "-a", filepath.Join(ro, "modem")+"/.", dst+"/").CombinedOutput(); err != nil {
			syscall.Unmount(ro, 0)
			return fmt.Errorf("copy persist/modem: %v: %s", err, out)
		}
	} else {
		log.Printf("vendor: persist has no modem directory")
	}
	syscall.Unmount(ro, syscall.MNT_DETACH)
	os.Remove(filepath.Join(s.nv, "persist.img"))
	return bind(filepath.Join(s.nv, "persist"), filepath.Join(mnt, "persist"), false)
}

func (s *Stack) backup(sums map[string]string) error {
	dir := filepath.Join(s.State, "nv-backup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	manifest := filepath.Join(dir, "SHA256SUMS.json")
	if _, err := os.Stat(manifest); err == nil {
		var first map[string]string
		if b, err := os.ReadFile(manifest); err == nil && json.Unmarshal(b, &first) == nil {
			for p, sum := range sums {
				if first[p] != "" && first[p] != sum {
					log.Printf("vendor: %s differs from the first backup", p)
				}
			}
		}
		return nil
	}
	for p := range sums {
		if strings.HasPrefix(p, "modem_") {
			continue
		}
		in, err := os.Open(filepath.Join(s.nv, p+".img"))
		if err != nil {
			return err
		}
		out, err := os.OpenFile(filepath.Join(dir, p+".img"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
		if err != nil {
			in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		in.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	b, _ := json.MarshalIndent(sums, "", "  ")
	tmp := manifest + ".tmp"
	if err := os.WriteFile(tmp, b, 0o400); err != nil {
		return err
	}
	syscall.Sync()
	log.Printf("vendor: first NV backup kept in %s", dir)
	return os.Rename(tmp, manifest)
}

func (s *Stack) setupProps(img string, boot map[string]string) error {
	values := map[string]string{}
	ReadBuildProp(filepath.Join(img, "system", "system", "build.prop"), values)
	ReadBuildProp(filepath.Join(img, "vendor", "build.prop"), values)
	for k, v := range ModemProps(boot, s.slot) {
		values[k] = v
	}
	bin := filepath.Join(s.root, values["vendor.modem.bin.path"])
	v, err := cp.BasebandVersion(bin)
	if err != nil {
		return fmt.Errorf("baseband version: %w", err)
	}
	values["persist.vendor.modem.baseband.version"] = v
	s.Baseband = v
	log.Printf("vendor: baseband %s", v)
	area, err := props.Create(filepath.Join(s.root, "dev", "__properties__"), 512*1024)
	if err != nil {
		return err
	}
	for k, val := range values {
		if err := area.Set(k, val); err != nil {
			log.Printf("vendor: property %s: %v", k, err)
		}
	}
	s.area = area
	os.MkdirAll(filepath.Join(s.root, "dev", "socket"), 0o755)
	svc, err := props.Serve(area.Area, filepath.Join(s.root, "dev", "socket", "property_service"))
	if err != nil {
		return err
	}
	svc.Observe = func(n, v string) { log.Printf("vendor: setprop %s=%s", n, v) }
	s.svc = svc
	return nil
}

func (s *Stack) setupDevices() error {
	dev := filepath.Join(s.root, "dev")
	for _, n := range []string{"null", "zero", "full", "random", "urandom", "kmsg", "umts_boot0", "umts_rfs0"} {
		if err := bind("/dev/"+n, filepath.Join(dev, n), false); err != nil {
			return err
		}
	}
	sys := filepath.Join(s.root, "sys")
	if err := mount("sysfs-skel", sys, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "mode=0755"); err != nil {
		return err
	}
	if err := bind("/sys/devices/platform/cpif", filepath.Join(sys, "devices", "platform", "cpif"), true); err != nil {
		return err
	}
	if err := bind("/sys/devices/platform/cpif", filepath.Join(sys, "bus", "platform", "devices", "cpif"), true); err != nil {
		return err
	}
	for _, n := range []string{"plat", "config"} {
		src := "/sys/firmware/devicetree/base/chosen/" + n
		if _, err := os.Stat(src); err != nil {
			log.Printf("vendor: no /chosen/%s in the device tree", n)
			continue
		}
		if err := bind(src, filepath.Join(sys, "firmware", "devicetree", "base", "chosen", n), true); err != nil {
			return err
		}
	}
	power := filepath.Join(s.Run, "power")
	os.MkdirAll(power, 0o700)
	for _, n := range []string{"wake_lock", "wake_unlock"} {
		f := filepath.Join(power, n)
		os.WriteFile(f, nil, 0o600)
		if err := bind(f, filepath.Join(sys, "power", n), false); err != nil {
			return err
		}
	}
	return mount("", sys, "", syscall.MS_REMOUNT|syscall.MS_RDONLY|syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "mode=0755")
}

func androidEnv() []string {
	return []string{
		"PATH=/system/bin:/vendor/bin",
		"LD_LIBRARY_PATH=/vendor/lib64:/vendor/lib64/hw:/system/lib64",
		"ANDROID_ROOT=/system",
		"ANDROID_DATA=/data",
	}
}

func (s *Stack) spawn(name string, args ...string) (*exec.Cmd, io.ReadCloser, error) {
	c := exec.Command(args[0], args[1:]...)
	c.Env = androidEnv()
	c.Dir = "/"
	c.SysProcAttr = &syscall.SysProcAttr{Chroot: s.root, Setpgid: true}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	c.Stdout = w
	c.Stderr = w
	if err := c.Start(); err != nil {
		w.Close()
		r.Close()
		return nil, nil, err
	}
	w.Close()
	s.mu.Lock()
	s.procs[name] = c
	s.mu.Unlock()
	return c, r, nil
}

func relay(name string, r io.Reader, watch func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		log.Printf("[%s] %s", name, line)
		if watch != nil {
			watch(line)
		}
	}
}

func (s *Stack) Start(cbdTimeout time.Duration) error {
	rfsd, out, err := s.spawn("rfsd", "/vendor/bin/rfsd")
	if err != nil {
		return err
	}
	rfsdDone := make(chan error, 1)
	go relay("rfsd", out, nil)
	go func() { rfsdDone <- rfsd.Wait() }()
	select {
	case err := <-rfsdDone:
		return fmt.Errorf("rfsd exited before the modem boot: %v", err)
	case <-time.After(2 * time.Second):
	}
	cbd, out, err := s.spawn("cbd", "/vendor/bin/cbd", "-t", s.ModemType, "-P", "by-name/modem"+s.slot, "-s", "2", "-o", "r")
	if err != nil {
		return err
	}
	failed := make(chan struct{}, 1)
	go relay("cbd", out, func(line string) {
		if strings.Contains(line, "start boot fail") || (strings.Contains(line, "ERR! IOCTL_") && strings.Contains(line, " fail")) {
			select {
			case failed <- struct{}{}:
			default:
			}
		}
	})
	cbdDone := make(chan error, 1)
	go func() { cbdDone <- cbd.Wait() }()
	var cbdErr error
	select {
	case cbdErr = <-cbdDone:
	case <-failed:
		syscall.Kill(-cbd.Process.Pid, syscall.SIGTERM)
		cbdErr = errors.New("cbd reported a failed boot attempt")
		<-cbdDone
	case <-time.After(cbdTimeout):
		syscall.Kill(-cbd.Process.Pid, syscall.SIGKILL)
		<-cbdDone
		cbdErr = errors.New("cbd timed out")
	}
	st, _ := cp.Status()
	if st != cp.Online {
		return fmt.Errorf("the modem is %s after cbd (%v)", st, cbdErr)
	}
	go func() {
		err := <-rfsdDone
		log.Printf("vendor: rfsd exited: %v", err)
	}()
	return nil
}

func (s *Stack) Stop() error {
	err := cp.PowerOff()
	s.mu.Lock()
	for _, c := range s.procs {
		if c.Process != nil {
			syscall.Kill(-c.Process.Pid, syscall.SIGTERM)
		}
	}
	s.mu.Unlock()
	if s.svc != nil {
		s.svc.Close()
	}
	return err
}
