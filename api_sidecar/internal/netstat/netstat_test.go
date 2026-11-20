package netstat

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLinuxReaderSelectsDefaultLinksAnd64BitCounters(t *testing.T) {
	root := t.TempDir()
	write := func(path, data string) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/net/route", "Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\nlo 00000000 00000000 0001 0 0 0 00000000 0 0 0\ndocker0 00000000 00000000 0001 0 0 0 00000000 0 0 0\neth0 00000000 0100000A 0003 0 0 100 00000000 0 0 0\neth1 00000000 0100000A 0003 0 0 200 00000000 0 0 0\nwg0 0000000A 00000000 0001 0 0 0 00FFFFFF 0 0 0\n")
	write("sys/class/net/eth0/ifindex", "7\n")
	write("sys/class/net/eth0/statistics/tx_bytes", "9000000000\n")
	write("sys/class/net/eth0/statistics/rx_bytes", "8000000000\n")
	snapshot, err := NewLinuxReader(root).Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Interfaces) != 1 || snapshot.Interfaces[0].Name != "eth0" || snapshot.Interfaces[0].Index != 7 || snapshot.Interfaces[0].Tx != 9000000000 || snapshot.Interfaces[0].Rx != 8000000000 {
		t.Fatalf("wrong default-link snapshot: %+v", snapshot)
	}
}

func TestSamplerRatesAndInvalidWindows(t *testing.T) {
	start := time.Now()
	snapshot := func(at time.Time, index uint32, tx, rx uint64) Snapshot {
		return Snapshot{At: at, Interfaces: []Counters{{Name: "eth0", Index: index, Tx: tx, Rx: rx}}}
	}
	sampler := NewSampler(10 * time.Second)
	if sampler.Sample(snapshot(start, 7, 0, 0)).Valid {
		t.Fatal("first sample is not a rate")
	}
	rate := sampler.Sample(snapshot(start.Add(10*time.Second), 7, 1250000000, 800000000))
	if !rate.Valid || rate.Up != 125000000 || rate.Down != 80000000 || !rate.WindowStart.Equal(start) || !rate.WindowEnd.Equal(start.Add(10*time.Second)) {
		t.Fatalf("NIC rates = %+v", rate)
	}
	for _, test := range []struct {
		name           string
		previous, next Snapshot
	}{
		{"reset", snapshot(start, 7, 100, 200), snapshot(start.Add(10*time.Second), 7, 101, 1)},
		{"interface replacement", snapshot(start, 7, 100, 200), snapshot(start.Add(10*time.Second), 8, 1000, 2000)},
		{"too soon", snapshot(start, 7, 100, 200), snapshot(start.Add(200*time.Millisecond), 7, 1000, 2000)},
		{"too old", snapshot(start, 7, 100, 200), snapshot(start.Add(31*time.Second), 7, 1000, 2000)},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := NewSampler(10 * time.Second)
			s.Sample(test.previous)
			if got := s.Sample(test.next); got.Valid {
				t.Fatalf("invalid rate accepted: %+v", got)
			}
		})
	}
}
