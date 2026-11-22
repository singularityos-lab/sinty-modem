package mm

import "testing"

func TestOnlyReadingTheSwitchesIsOpen(t *testing.T) {
	for method := range networkMethods(nil) {
		want := actionDeviceControl
		if method == "GetSettings" {
			want = ""
		}
		if got := actionFor(ifNetwork, method); got != want {
			t.Errorf("%s asks for %q", method, got)
		}
	}
}
