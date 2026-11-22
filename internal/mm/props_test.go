package mm

import (
	"testing"

	"github.com/singularityos-lab/sinty-modem/internal/phone"
)

func TestPropertiesHaveTheTypesModemManagerClientsRead(t *testing.T) {
	e := &Exporter{}
	if got := e.props3gpp(phone.Status{})["Pco"].Signature().String(); got != "a(ubay)" {
		t.Fatalf("Pco is %s", got)
	}
	if got := e.simProps(phone.Status{})["PreferredNetworks"].Signature().String(); got != "a(su)" {
		t.Fatalf("PreferredNetworks is %s", got)
	}
}

func TestAnEmbeddedSIMIsAnESIMThatCannotBeRemoved(t *testing.T) {
	e := &Exporter{}
	sim := e.simProps(phone.Status{SIM: phone.SIM{Embedded: true}})
	if sim["SimType"].Value() != uint32(2) || sim["Removability"].Value() != uint32(2) {
		t.Fatalf("embedded SIM shown as type %v removability %v", sim["SimType"].Value(), sim["Removability"].Value())
	}
	card := e.simProps(phone.Status{})
	if card["SimType"].Value() != uint32(1) || card["Removability"].Value() != uint32(1) {
		t.Fatalf("card shown as type %v removability %v", card["SimType"].Value(), card["Removability"].Value())
	}
}
