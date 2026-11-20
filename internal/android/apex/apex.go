package apex

import (
	"archive/zip"
	"errors"
	"fmt"
	"os"
)

func Payload(path string) (offset, size int64, err error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return 0, 0, err
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name != "apex_payload.img" {
			continue
		}
		if f.Method != zip.Store {
			return 0, 0, fmt.Errorf("apex %s: the payload is compressed", path)
		}
		off, err := f.DataOffset()
		if err != nil {
			return 0, 0, err
		}
		return off, int64(f.UncompressedSize64), nil
	}
	return 0, 0, errors.New("apex: no apex_payload.img in " + path)
}

func Open(path string) (*os.File, int64, int64, error) {
	off, size, err := Payload(path)
	if err != nil {
		return nil, 0, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	return f, off, size, nil
}
