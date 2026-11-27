//go:build !windows

package userpath

import (
	"errors"
	"testing"
)

func TestOtherPlatformsDoNotEditShellProfiles(t *testing.T) {
	manager, err := New()
	if manager != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("New() = %v, %v", manager, err)
	}
}
