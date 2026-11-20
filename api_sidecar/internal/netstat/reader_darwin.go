package netstat

import (
	"encoding/binary"
	"errors"
	"net"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

type darwinReader struct{}

func NewReader(string) Reader { return darwinReader{} }

func (darwinReader) Read() (Snapshot, error) {
	rib, err := route.FetchRIB(syscall.AF_UNSPEC, route.RIBTypeRoute, 0)
	if err != nil {
		return Snapshot{}, err
	}
	messages, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return Snapshot{}, err
	}
	names := map[int]string{}
	for _, message := range messages {
		routeMessage, ok := message.(*route.RouteMessage)
		if !ok || routeMessage.Index == 0 || routeMessage.Flags&syscall.RTF_UP == 0 || routeMessage.Flags&syscall.RTF_HOST != 0 || len(routeMessage.Addrs) <= syscall.RTAX_NETMASK {
			continue
		}
		if !zeroRouteAddress(routeMessage.Addrs[syscall.RTAX_DST]) || !zeroRouteAddress(routeMessage.Addrs[syscall.RTAX_NETMASK]) {
			continue
		}
		iface, lookupErr := net.InterfaceByIndex(routeMessage.Index)
		if lookupErr != nil {
			return Snapshot{}, lookupErr
		}
		if !excludedInterface(iface.Name) {
			names[iface.Index] = iface.Name
		}
	}
	if len(names) == 0 {
		return Snapshot{}, errors.New("no default-route interface")
	}
	// NET_RT_IFLIST2 carries IfData64; NET_RT_IFLIST exposes 32-bit byte counters on Darwin.
	raw, err := unix.SysctlRaw("net.route.0.0.6.0")
	if err != nil {
		return Snapshot{}, err
	}
	counters, err := parseDarwinCounters(raw, names)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{At: time.Now(), Interfaces: counters}, nil
}

func zeroRouteAddress(address route.Addr) bool {
	switch value := address.(type) {
	case nil:
		return true
	case *route.Inet4Addr:
		return value.IP == [4]byte{}
	case *route.Inet6Addr:
		return value.IP == [16]byte{}
	default:
		return false
	}
}

func parseDarwinCounters(raw []byte, names map[int]string) ([]Counters, error) {
	var header unix.IfMsghdr2
	var data unix.IfData64
	indexOffset := int(unsafe.Offsetof(header.Index))
	dataOffset := int(unsafe.Offsetof(header.Data))
	txOffset, rxOffset := dataOffset+int(unsafe.Offsetof(data.Obytes)), dataOffset+int(unsafe.Offsetof(data.Ibytes))
	var result []Counters
	for len(raw) > 0 {
		if len(raw) < 4 {
			return nil, errors.New("truncated interface message")
		}
		length := int(binary.NativeEndian.Uint16(raw[:2]))
		if length < 4 || length > len(raw) {
			return nil, errors.New("invalid interface message length")
		}
		message := raw[:length]
		raw = raw[length:]
		if message[3] != unix.RTM_IFINFO2 {
			continue
		}
		if len(message) < unix.SizeofIfMsghdr2 {
			return nil, errors.New("truncated 64-bit interface counters")
		}
		index := int(binary.NativeEndian.Uint16(message[indexOffset : indexOffset+2]))
		if name, ok := names[index]; ok {
			result = append(result, Counters{Name: name, Index: uint32(index), Tx: binary.NativeEndian.Uint64(message[txOffset : txOffset+8]), Rx: binary.NativeEndian.Uint64(message[rxOffset : rxOffset+8])})
		}
	}
	if len(result) != len(names) {
		return nil, errors.New("default-route interface counters incomplete")
	}
	return result, nil
}
