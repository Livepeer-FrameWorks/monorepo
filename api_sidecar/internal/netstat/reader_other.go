//go:build !linux && !darwin

package netstat

import "errors"

type unsupportedReader struct{}

func NewReader(string) Reader { return unsupportedReader{} }
func (unsupportedReader) Read() (Snapshot, error) {
	return Snapshot{}, errors.New("interface counters unavailable on this platform")
}
