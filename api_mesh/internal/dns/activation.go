package dns

import (
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/systemd"
)

// systemdSockets returns the UDP and TCP sockets systemd passed through
// socket activation (frameworks-privateer.socket). Both are nil when the
// process was not socket-activated, which is the dev/compose/macOS case.
func systemdSockets() (net.PacketConn, net.Listener, error) {
	files := systemd.ListenFDs()
	if len(files) == 0 {
		return nil, nil, nil
	}
	return socketsFromFiles(files)
}

// socketsFromFiles picks the first datagram and first stream socket out of
// the passed descriptors. net.FileListener and net.FilePacketConn duplicate
// the descriptor, so the originals are closed here; systemd keeps its own
// copy open across service restarts.
func socketsFromFiles(files []*os.File) (net.PacketConn, net.Listener, error) {
	var (
		packetConn net.PacketConn
		listener   net.Listener
		errs       []error
	)
	for _, f := range files {
		if listener == nil {
			if ln, err := net.FileListener(f); err == nil {
				listener = ln
				_ = f.Close()
				continue
			}
		}
		if packetConn == nil {
			if pc, err := net.FilePacketConn(f); err == nil {
				packetConn = pc
				_ = f.Close()
				continue
			}
		}
		errs = append(errs, fmt.Errorf("passed descriptor %s is not a usable DNS socket", f.Name()))
		_ = f.Close()
	}
	return packetConn, listener, errors.Join(errs...)
}
