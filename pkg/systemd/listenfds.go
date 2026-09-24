// Package systemd implements the parts of systemd's process protocols that
// services use directly.
package systemd

import (
	"os"
	"strconv"
	"syscall"
)

// listenFDsStart is the first descriptor systemd passes to a socket-activated
// process (SD_LISTEN_FDS_START).
const listenFDsStart = 3

// ListenFDs returns the sockets systemd passed through socket activation, in
// the order the socket unit lists them, or nil when the process was not
// socket-activated. It implements the sd_listen_fds(3) handshake: the
// descriptors belong to this process only when LISTEN_PID names it. The
// LISTEN_* variables are cleared once read so child processes do not inherit
// them, and the descriptors are marked close-on-exec.
func ListenFDs() []*os.File {
	count := listenFDCount(os.Getenv("LISTEN_PID"), os.Getenv("LISTEN_FDS"), os.Getpid())
	_ = os.Unsetenv("LISTEN_PID")
	_ = os.Unsetenv("LISTEN_FDS")
	_ = os.Unsetenv("LISTEN_FDNAMES")
	if count == 0 {
		return nil
	}
	files := make([]*os.File, 0, count)
	for fd := listenFDsStart; fd < listenFDsStart+count; fd++ {
		syscall.CloseOnExec(fd)
		files = append(files, os.NewFile(uintptr(fd), "LISTEN_FD_"+strconv.Itoa(fd)))
	}
	return files
}

func listenFDCount(listenPID, listenFDs string, pid int) int {
	owner, err := strconv.Atoi(listenPID)
	if err != nil || owner != pid {
		return 0
	}
	count, err := strconv.Atoi(listenFDs)
	if err != nil || count < 0 {
		return 0
	}
	return count
}
