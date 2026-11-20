package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/phone"
)

type Store struct {
	dir string
	mu  sync.Mutex
}

func Open(dir string) (*Store, error) {
	for _, d := range []string{dir, filepath.Join(dir, "parts"), filepath.Join(dir, "raw")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	return &Store{dir: dir}, nil
}

func NewID() string {
	var b [12]byte
	rand.Read(b[:])
	return time.Now().UTC().Format("20060102150405") + "-" + hex.EncodeToString(b[:])
}

func writeSync(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func validID(id string) bool {
	if id == "" || len(id) > 80 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c == '-') {
			return false
		}
	}
	return true
}

func (s *Store) KeepRaw(raw []byte) (string, error) {
	id := NewID()
	return id, writeSync(filepath.Join(s.dir, "raw", id+".sit"), raw)
}

func (s *Store) Raw() map[string][]byte {
	out := map[string][]byte{}
	entries, _ := os.ReadDir(filepath.Join(s.dir, "raw"))
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".sit")
		if !ok || !validID(id) {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(s.dir, "raw", e.Name())); err == nil {
			out[id] = b
		}
	}
	return out
}

func (s *Store) DropRaw(id string) {
	if validID(id) {
		os.Remove(filepath.Join(s.dir, "raw", id+".sit"))
	}
}

func (s *Store) Put(m phone.Message) error {
	if !validID(m.ID) {
		return errors.New("invalid message id")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeSync(filepath.Join(s.dir, m.ID+".json"), b)
}

func (s *Store) Delete(id string) error {
	if !validID(id) {
		return errors.New("invalid message id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(filepath.Join(s.dir, id+".json"))
	if os.IsNotExist(err) {
		return errors.New("no such message")
	}
	return err
}

func (s *Store) List() []phone.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _ := os.ReadDir(s.dir)
	var out []phone.Message
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var m phone.Message
		if json.Unmarshal(b, &m) == nil && validID(m.ID) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	return out
}

type Part struct {
	Sender string
	Ref    int
	Total  int
	Seq    int
	Text   string
	Data   []byte
	Time   time.Time
	Got    time.Time
}

func (s *Store) AddPart(p Part) ([]Part, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.dir, "parts")
	b, _ := json.Marshal(p)
	name := hex.EncodeToString([]byte(p.Sender)) + "-" + strconv.Itoa(p.Ref) + "-" + strconv.Itoa(p.Total) + "-" + strconv.Itoa(p.Seq) + ".json"
	if err := writeSync(filepath.Join(dir, name), b); err != nil {
		return nil, err
	}
	prefix := hex.EncodeToString([]byte(p.Sender)) + "-" + strconv.Itoa(p.Ref) + "-" + strconv.Itoa(p.Total) + "-"
	entries, _ := os.ReadDir(dir)
	got := map[int]Part{}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		var q Part
		if bb, err := os.ReadFile(full); err == nil && json.Unmarshal(bb, &q) == nil {
			if time.Since(q.Got) > 7*24*time.Hour {
				os.Remove(full)
				continue
			}
			if strings.HasPrefix(e.Name(), prefix) {
				got[q.Seq] = q
			}
		}
	}
	if len(got) < p.Total {
		return nil, nil
	}
	out := make([]Part, 0, p.Total)
	for i := 1; i <= p.Total; i++ {
		q, ok := got[i]
		if !ok {
			return nil, nil
		}
		out = append(out, q)
	}
	for i := 1; i <= p.Total; i++ {
		os.Remove(filepath.Join(dir, prefix+strconv.Itoa(i)+".json"))
	}
	return out, nil
}
