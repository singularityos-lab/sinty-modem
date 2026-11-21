package vendor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLatchCountsWithinOneBuild(t *testing.T) {
	l := Latch{Path: filepath.Join(t.TempDir(), "state", "attempts"), Build: "26A170"}
	for want := 1; want <= 3; want++ {
		n, err := l.Arm()
		if err != nil {
			t.Fatal(err)
		}
		if n != want || l.Attempts() != want {
			t.Fatalf("attempt %d: armed %d, read %d", want, n, l.Attempts())
		}
	}
	if err := l.Clear(); err != nil {
		t.Fatal(err)
	}
	if l.Attempts() != 0 {
		t.Fatal("cleared latch still counts")
	}
}

func TestLatchStartsOverOnANewBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempts")
	old := Latch{Path: path, Build: "26A167"}
	for i := 0; i < 3; i++ {
		old.Arm()
	}
	if (Latch{Path: path, Build: "26A170"}).Attempts() != 0 {
		t.Fatal("a new build inherited the old failures")
	}
	if old.Attempts() != 3 {
		t.Fatal("the same build lost its failures")
	}
}

func TestLatchFromBeforeBuildsWereRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempts")
	if err := os.WriteFile(path, []byte("3 2026-11-21T08:24:00+01:00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := (Latch{Path: path, Build: "26A170"}).Attempts(); n != 0 {
		t.Fatalf("a latch without a build blocks a new build: %d", n)
	}
}

func TestOSBuild(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "os-release")
	os.WriteFile(p, []byte("NAME=\"Sinty OS\"\nBUILD_ID=26A170\n"), 0o644)
	if got := OSBuild(filepath.Join(dir, "missing"), p); got != "26A170" {
		t.Fatalf("got %q", got)
	}
}
