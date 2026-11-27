//go:build !windows

package userpath

// New does not modify Unix shell profiles or infer a platform-specific installation.
func New() (*Manager, error) { return nil, ErrUnsupported }
