package apn

import (
	"encoding/xml"
	"os"
	"strings"
)

const Database = "/usr/share/mobile-broadband-provider-info/serviceproviders.xml"

type usage struct {
	Type string `xml:"type,attr"`
}

type apnEntry struct {
	Value  string  `xml:"value,attr"`
	Usage  []usage `xml:"usage"`
	Name   string  `xml:"name"`
	IPType struct {
		Type string `xml:"type,attr"`
	} `xml:"ip-type"`
}

type networkID struct {
	MCC string `xml:"mcc,attr"`
	MNC string `xml:"mnc,attr"`
}

type provider struct {
	Name []string `xml:"name"`
	GSM  *struct {
		NetworkID []networkID `xml:"network-id"`
		APN       []apnEntry  `xml:"apn"`
	} `xml:"gsm"`
}

type db struct {
	Country []struct {
		Code     string     `xml:"code,attr"`
		Provider []provider `xml:"provider"`
	} `xml:"country"`
}

type Carrier struct {
	Name     string
	Internet string
	IPType   string
	Attach   string
	IMS      string
	MMS      string
}

func usageOf(a apnEntry) map[string]bool {
	out := map[string]bool{}
	for _, u := range a.Usage {
		out[u.Type] = true
	}
	return out
}

func Lookup(path, mcc string, mncs []string) (*Carrier, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var d db
	if err := xml.NewDecoder(f).Decode(&d); err != nil {
		return nil, err
	}
	for _, mnc := range mncs {
		for _, c := range d.Country {
			for _, p := range c.Provider {
				if p.GSM == nil {
					continue
				}
				match := false
				for _, n := range p.GSM.NetworkID {
					if n.MCC == mcc && strings.TrimLeft(n.MNC, "0") == strings.TrimLeft(mnc, "0") && len(n.MNC) == len(mnc) {
						match = true
					}
				}
				if !match {
					continue
				}
				car := &Carrier{}
				if len(p.Name) > 0 {
					car.Name = strings.TrimSpace(p.Name[0])
				}
				for _, a := range p.GSM.APN {
					u := usageOf(a)
					v := strings.TrimSpace(a.Value)
					switch {
					case u["internet"] && car.Internet == "":
						car.Internet = v
						car.IPType = strings.ToLower(a.IPType.Type)
					case u["ia"] && car.Attach == "":
						car.Attach = v
					case u["ims"] && car.IMS == "":
						car.IMS = v
					case u["mms"] && car.MMS == "":
						car.MMS = v
					}
				}
				if car.Internet != "" {
					return car, nil
				}
			}
		}
	}
	return nil, nil
}
