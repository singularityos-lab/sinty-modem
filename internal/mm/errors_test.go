package mm

import (
	"errors"
	"fmt"
	"testing"

	"github.com/singularityos-lab/sinty-modem/internal/phone"
)

func TestNoNetworkHasItsOwnErrorName(t *testing.T) {
	if got := fail("Failed", fmt.Errorf("dial: %w", phone.ErrNotReady)).Name; got != errCore+"NotRegistered" {
		t.Fatalf("got %s", got)
	}
	if got := fail("Failed", errors.New("other")).Name; got != errCore+"Failed" {
		t.Fatalf("got %s", got)
	}
}
