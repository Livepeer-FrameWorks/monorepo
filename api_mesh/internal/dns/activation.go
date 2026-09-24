package dns

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
)

// sdListenFDsStart is the first descriptor systemd passes to a
// socket-activated process (SD_LISTEN_FDS_START).
const sdListenFDsStart = 3

// systemdSockets returns the UDP and TCP sockets systemd passed through
// socket activation (frameworks-privateer.socket). Both are nil when the
// process was not socket-activated, which is the dev/compose/macOS case.
// The LISTEN_* variables are cleared once consumed so they are not
// inherited by child processes.
func systemdSockets() (net.PacketConn, net.Listener, error) {
	count := listenFDCount(os.Getenv, os.Getpid())
	_ = os.Unsetenv("LISTEN_PID")
	_ = os.Unsetenv("LISTEN_FDS")
	_ = os.Unsetenv("LISTEN_FDNAMES")
	if count == 0 {
		return nil, nil, nil
	}
	files := make([]*os.File, 0, count)
	for fd := sdListenFDsStart; fd < sdListenFDsStart+count; fd++ {
		syscall.CloseOnExec(fd)
		files = append(files, os.NewFile(uintptr(fd), "LISTEN_FD_"+strconv.Itoa(fd)))
	}
	return socketsFromFiles(files)
}

// listenFDCount implements the sd_listen_fds(3) handshake: the descriptors
// belong to this process only when LISTEN_PID names it.
func listenFDCount(getenv func(string) string, pid int) int {
	listenPID, err := strconv.Atoi(getenv("LISTEN_PID"))
	if err != nil || listenPID != pid {
		return 0
	}
	count, err := strconv.Atoi(getenv("LISTEN_FDS"))
	if err != nil || count < 0 {
		return 0
	}
	return count
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
