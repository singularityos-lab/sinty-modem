package vendor

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/singularityos-lab/sinty-modem/internal/android/dm"
	"github.com/singularityos-lab/sinty-modem/internal/android/lp"
)

const sysBlock = "/sys/class/block"

type Block struct {
	Name     string
	Kernel   string
	Major    uint32
	Minor    uint32
	ReadOnly bool
}

func FindPartition(label string) (Block, error) {
	entries, err := os.ReadDir(sysBlock)
	if err != nil {
		return Block{}, err
	}
	for _, e := range entries {
		ue, err := os.ReadFile(filepath.Join(sysBlock, e.Name(), "uevent"))
		if err != nil {
			continue
		}
		found := false
		var major, minor uint64
		for _, line := range strings.Split(string(ue), "\n") {
			k, v, _ := strings.Cut(line, "=")
			switch k {
			case "PARTNAME":
				found = v == label
			case "MAJOR":
				major, _ = strconv.ParseUint(v, 10, 32)
			case "MINOR":
				minor, _ = strconv.ParseUint(v, 10, 32)
			}
		}
		if !found {
			continue
		}
		ro, _ := os.ReadFile(filepath.Join(sysBlock, e.Name(), "ro"))
		return Block{
			Name:     label,
			Kernel:   e.Name(),
			Major:    uint32(major),
			Minor:    uint32(minor),
			ReadOnly: strings.TrimSpace(string(ro)) == "1",
		}, nil
	}
	return Block{}, fmt.Errorf("no partition labelled %s", label)
}

func mkdev(major, minor uint32) int {
	return int(uint64(minor&0xff) | uint64(major&0xfff)<<8 | uint64(minor&^0xff)<<12)
}

func Node(dir, name string, major, minor uint32) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	os.Remove(p)
	if err := syscall.Mknod(p, syscall.S_IFBLK|0o600, mkdev(major, minor)); err != nil {
		return "", fmt.Errorf("mknod %s: %w", p, err)
	}
	return p, nil
}

func (b Block) Node(dir string) (string, error) { return Node(dir, b.Kernel, b.Major, b.Minor) }

func (b Block) Mounted() bool {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	want := fmt.Sprintf(" %d:%d ", b.Major, b.Minor)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

func MapDynamic(meta *lp.Metadata, super Block, part, dmName, nodeDir string) (string, error) {
	p, ok := meta.Find(part)
	if !ok {
		return "", fmt.Errorf("no dynamic partition %s", part)
	}
	if len(p.Extents) == 0 {
		return "", fmt.Errorf("dynamic partition %s is empty", part)
	}
	var targets []dm.Target
	var start uint64
	for _, e := range p.Extents {
		switch e.Type {
		case lp.TargetLinear:
			if e.Source != 0 {
				return "", fmt.Errorf("%s spans a second block device, which is not supported", part)
			}
			targets = append(targets, dm.Target{Start: start, Length: e.Sectors, Type: "linear", Params: fmt.Sprintf("%d:%d %d", super.Major, super.Minor, e.Data)})
		case lp.TargetZero:
			targets = append(targets, dm.Target{Start: start, Length: e.Sectors, Type: "zero"})
		default:
			return "", fmt.Errorf("%s has an extent of unknown type %d", part, e.Type)
		}
		start += e.Sectors
	}
	dm.Remove(dmName)
	d, err := dm.Create(dmName, targets, true)
	if err != nil {
		return "", err
	}
	return Node(nodeDir, dmName, d.Major, d.Minor)
}

func CopyDevice(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.CopyBuffer(io.MultiWriter(out, h), in, make([]byte, 4<<20)); err != nil {
		out.Close()
		return "", fmt.Errorf("copy %s: %w", src, err)
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
