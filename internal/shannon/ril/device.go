package ril

import (
	"errors"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

func (r *RIL) IMEI() (string, error) {
	p, err := r.c.Request(sit.GetIMEI, nil, 8*time.Second)
	if err != nil {
		return "", err
	}
	return decodeIMEI(p)
}

func decodeIMEI(p []byte) (string, error) {
	if len(p) < 1 {
		return "", errors.New("unexpected IMEI layout")
	}
	n := int(p[0])
	if n < 14 || n > 16 || len(p) < 1+n {
		return "", errors.New("unexpected IMEI layout")
	}
	for _, c := range p[1 : 1+n] {
		if c < '0' || c > '9' {
			return "", errors.New("unexpected IMEI digits")
		}
	}
	return string(p[1 : 1+n]), nil
}
