package netstat

import (
	"encoding/binary"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestDarwinInterfaceCountersPreserve64Bits(t *testing.T) {
	var header unix.IfMsghdr2
	var data unix.IfData64
	message := make([]byte, unix.SizeofIfMsghdr2)
	binary.NativeEndian.PutUint16(message[:2], uint16(len(message)))
	message[3] = unix.RTM_IFINFO2
	index := int(unsafe.Offsetof(header.Index))
	binary.NativeEndian.PutUint16(message[index:index+2], 7)
	base := int(unsafe.Offsetof(header.Data))
	tx, rx := base+int(unsafe.Offsetof(data.Obytes)), base+int(unsafe.Offsetof(data.Ibytes))
	binary.NativeEndian.PutUint64(message[tx:tx+8], 9000000000)
	binary.NativeEndian.PutUint64(message[rx:rx+8], 8000000000)
	counters, err := parseDarwinCounters(message, map[int]string{7: "en0"})
	if err != nil || len(counters) != 1 || counters[0].Tx != 9000000000 || counters[0].Rx != 8000000000 {
		t.Fatalf("64-bit interface counters=%v err=%v", counters, err)
	}
}
