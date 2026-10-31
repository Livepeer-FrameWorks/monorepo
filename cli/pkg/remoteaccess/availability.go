package remoteaccess

import (
	"context"
	"errors"
	"net"
	"syscall"
)

// IsUnavailable recognizes transport outages without classifying manifest,
// authentication or host-key failures as recoverable control-plane downtime.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if classified, ok := err.(interface{ Unavailable() bool }); ok {
		return classified.Unavailable()
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		failures := joined.Unwrap()
		if len(failures) == 0 {
			return false
		}
		for _, failure := range failures {
			if !IsUnavailable(failure) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if cause := wrapped.Unwrap(); cause != nil {
			return IsUnavailable(cause)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return !dnsErr.IsNotFound && (dnsErr.IsTimeout || dnsErr.IsTemporary)
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.ECONNREFUSED || errno == syscall.EHOSTUNREACH || errno == syscall.ENETUNREACH || errno == syscall.ETIMEDOUT || errno == syscall.ECONNRESET || errno == syscall.EHOSTDOWN || errno == syscall.ENETDOWN || errno == syscall.EPIPE
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) && networkErr.Timeout()
}
