package vendor

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDeviceNodesOpenUnderANodevRun(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to mount and create device nodes")
	}
	run := t.TempDir()
	if err := mount("run", run, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=0700"); err != nil {
		t.Skipf("cannot mount tmpfs: %v", err)
	}
	defer syscall.Unmount(run, syscall.MNT_DETACH)
	nodes := filepath.Join(run, "nodes")
	if err := mountNodes(nodes); err != nil {
		t.Fatal(err)
	}
	defer syscall.Unmount(nodes, syscall.MNT_DETACH)
	p := filepath.Join(nodes, "null")
	if err := syscall.Mknod(p, syscall.S_IFCHR|0o600, mkdev(1, 3)); err != nil {
		t.Skipf("cannot create device nodes here: %v", err)
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("device node under the nodes mount does not open: %v", err)
	}
	f.Close()
}
