package mm

const (
	mode2G uint32 = 1 << 1
	mode3G uint32 = 1 << 2
	mode4G uint32 = 1 << 3
)

type modes struct{ Allowed, Preferred uint32 }

var (
	modesAutomatic = modes{mode2G | mode3G | mode4G, mode4G}
	modesLTEOnly   = modes{mode4G, 0}
)

func currentModes(lteOnly bool) modes {
	if lteOnly {
		return modesLTEOnly
	}
	return modesAutomatic
}

func lteOnlyMode(m modes) (lteOnly, ok bool) {
	return m == modesLTEOnly, m == modesLTEOnly || m == modesAutomatic
}
