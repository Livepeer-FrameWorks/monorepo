package cmd

import (
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// procNetMarker heads each /proc/net socket table hostListenerShell prints
// when the host has no ss.
const procNetMarker = "@@proc_net"

// hostListenerShell lists a host's TCP listeners and bound UDP sockets as
// `ss -Hltun[p]` lines, with the owning process when it may run ss as root.
// A host without ss (a minimal image or container) prints its /proc/net socket
// tables instead; normalizeHostListeners turns those into the same lines.
const hostListenerShell = `if command -v ss >/dev/null 2>&1; then sudo -n ss -Hltunp 2>/dev/null || ss -Hltun; else for t in tcp tcp6 udp udp6; do echo "` + procNetMarker + ` $t"; cat /proc/net/$t 2>/dev/null; done; fi`

// normalizeHostListeners returns hostListenerShell output as `ss -Hltun`
// lines: ss output unchanged, /proc/net tables as one line per TCP socket in
// LISTEN and per unconnected UDP socket.
func normalizeHostListeners(output string) string {
	if !strings.Contains(output, procNetMarker) {
		return output
	}
	var b strings.Builder
	table := ""
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == procNetMarker {
			table = fields[1]
			continue
		}
		// sl local_address rem_address st ...
		if len(fields) < 4 || fields[0] == "sl" {
			continue
		}
		proto, state := "", ""
		switch {
		case strings.HasPrefix(table, "tcp") && fields[3] == "0A":
			proto, state = "tcp", "LISTEN"
		case strings.HasPrefix(table, "udp") && fields[3] == "07":
			proto, state = "udp", "UNCONN"
		default:
			continue
		}
		local, ok := procNetAddress(fields[1])
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%s %s 0 0 %s *:*\n", proto, state, local)
	}
	return b.String()
}

// procNetAddress decodes a /proc/net local_address (hex address in host byte
// order per 32-bit word, then the hex port) into ss's address:port form.
func procNetAddress(field string) (string, bool) {
	addrHex, portHex, ok := strings.Cut(field, ":")
	if !ok {
		return "", false
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return "", false
	}
	raw, err := hex.DecodeString(addrHex)
	if err != nil || (len(raw) != net.IPv4len && len(raw) != net.IPv6len) {
		return "", false
	}
	for word := 0; word < len(raw); word += 4 {
		raw[word], raw[word+1], raw[word+2], raw[word+3] = raw[word+3], raw[word+2], raw[word+1], raw[word]
	}
	ip := net.IP(raw)
	if len(raw) == net.IPv6len {
		return "[" + ip.String() + "]:" + strconv.FormatUint(port, 10), true
	}
	return ip.String() + ":" + strconv.FormatUint(port, 10), true
}
