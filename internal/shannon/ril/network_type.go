package ril

import (
	"encoding/binary"
	"time"

	"github.com/singularityos-lab/sinty-modem/internal/shannon/sit"
)

const (
	ratGPRS = 1 << (iota + 1)
	ratEDGE
	ratUMTS
	ratHSDPA
	ratHSUPA
	ratHSPA
	ratLTE
	ratHSPAPlus
	ratGSM
	ratTDSCDMA
)

func networkTypePayload(lteOnly bool) []byte {
	mask := uint32(ratGPRS | ratEDGE | ratUMTS | ratHSDPA | ratHSUPA | ratHSPA | ratLTE | ratHSPAPlus | ratGSM | ratTDSCDMA)
	if lteOnly {
		mask = ratLTE
	}
	return binary.LittleEndian.AppendUint32(nil, mask)
}

func (r *RIL) SetLTEOnly(on bool) error {
	_, err := r.c.Request(sit.SetNetworkTypes, networkTypePayload(on), 30*time.Second)
	return err
}
