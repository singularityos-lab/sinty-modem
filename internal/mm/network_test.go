package mm

import "testing"

func TestOnlyReadingTheSwitchesAndTheByteCountIsOpen(t *testing.T) {
	for method := range networkMethods(nil) {
		want := actionDeviceControl
		if method == "GetSettings" || method == "GetUsage" {
			want = ""
		}
		if got := actionFor(ifNetwork, method); got != want {
			t.Errorf("%s asks for %q", method, got)
		}
	}
}
