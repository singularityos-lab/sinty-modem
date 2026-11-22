package esim

import (
	"errors"
	"slices"
	"testing"
)

func TestOneFailedNoticeDoesNotHoldTheOthers(t *testing.T) {
	var sent []int
	unreachable := errors.New("carrier unreachable")
	err := sendEach([]int{1, 2, 3}, func(n int) error {
		sent = append(sent, n)
		if n == 1 {
			return unreachable
		}
		return nil
	})
	if !errors.Is(err, unreachable) || !slices.Equal(sent, []int{1, 2, 3}) {
		t.Fatalf("sent %v, error %v", sent, err)
	}
}
