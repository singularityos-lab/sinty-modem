package vendor

import (
	"bufio"
	"os"
	"strings"
)

func ReadBuildProp(path string, into map[string]string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64<<10), 1<<20)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || line[0] == '#' || strings.HasPrefix(line, "import ") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, set := into[k]; set && strings.HasPrefix(k, "ro.") {
			continue
		}
		into[k] = strings.TrimSpace(v)
	}
}

func ModemProps(boot map[string]string, slot string) map[string]string {
	p := map[string]string{
		"ro.build.type":                           "user",
		"ro.vendor.build.type":                    "user",
		"ro.debuggable":                           "0",
		"ro.bootmode":                             "normal",
		"ro.property_service.version":             "2",
		"persist.radio.multisim.config":           "dsds",
		"vendor.cbd.partition":                    "modem" + slot,
		"vendor.cbd.modem_bin_type":               "0",
		"vendor.modem.bin.path":                   "/mnt/vendor/modem_img/images/default/modem.bin",
		"persist.vendor.modem.efs.clear.action":   "none",
		"persist.vendor.cbd.modem.bootmode":       "",
		"persist.vendor.modem.recovery.enabled":   "0",
		"persist.vendor.radio.no_modem_board":     "0",
		"persist.vendor.sys.modem.logging.enable": "false",
		"vendor.sys.modem.logging.enable":         "false",
	}
	for k, v := range boot {
		p[k] = v
	}
	p["ro.boot.slot_suffix"] = slot
	if hw := boot["ro.boot.hardware"]; hw != "" {
		p["ro.hardware"] = hw
	}
	if plat := boot["ro.boot.hardware.platform"]; plat != "" {
		p["ro.board.platform"] = plat
	}
	if id := boot["ro.boot.cdt_hwid"]; id != "" {
		p["persist.vendor.modem.cdt_hwid"] = id
	}
	return p
}
