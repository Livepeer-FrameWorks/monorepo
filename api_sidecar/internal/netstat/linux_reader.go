package netstat

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type LinuxReader struct{ root string }

func NewLinuxReader(root string) *LinuxReader {
	if root == "" {
		root = "/"
	}
	return &LinuxReader{root: root}
}

func (r *LinuxReader) Read() (Snapshot, error) {
	selected := map[string]struct{}{}
	var failures []error
	for _, family := range []string{"route", "ipv6_route"} {
		data, err := os.ReadFile(filepath.Join(r.root, "proc/net", family))
		if err != nil {
			failures = append(failures, err)
			continue
		}
		links := defaultRouteInterfaces(string(data), family == "ipv6_route")
		for _, name := range links {
			selected[name] = struct{}{}
		}
	}
	if len(selected) == 0 {
		if len(failures) == 0 {
			return Snapshot{}, errors.New("no default-route interface")
		}
		return Snapshot{}, fmt.Errorf("no default-route interface: %w", errors.Join(failures...))
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	read := func(name, path string) (uint64, error) {
		data, err := os.ReadFile(filepath.Join(r.root, "sys/class/net", name, path))
		if err != nil {
			return 0, err
		}
		return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	}
	result := Snapshot{Interfaces: make([]Counters, 0, len(names))}
	for _, name := range names {
		index, err := read(name, "ifindex")
		if err != nil {
			return Snapshot{}, fmt.Errorf("read %s interface index: %w", name, err)
		}
		if index == 0 || index > 1<<32-1 {
			return Snapshot{}, fmt.Errorf("invalid interface index %d for %s", index, name)
		}
		tx, err := read(name, "statistics/tx_bytes")
		if err != nil {
			return Snapshot{}, fmt.Errorf("read %s egress: %w", name, err)
		}
		rx, err := read(name, "statistics/rx_bytes")
		if err != nil {
			return Snapshot{}, fmt.Errorf("read %s ingress: %w", name, err)
		}
		result.Interfaces = append(result.Interfaces, Counters{Name: name, Index: uint32(index), Tx: tx, Rx: rx})
	}
	result.At = time.Now()
	return result, nil
}

func defaultRouteInterfaces(data string, ipv6 bool) []string {
	var names []string
	best := uint64(^uint64(0))
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		var name, metricText, flagsText string
		base := 10
		if ipv6 {
			if len(fields) < 10 || fields[0] != strings.Repeat("0", 32) || fields[1] != "00" {
				continue
			}
			name, metricText, flagsText, base = fields[9], fields[5], fields[8], 16
		} else {
			if len(fields) < 8 || fields[1] != "00000000" || fields[7] != "00000000" {
				continue
			}
			name, metricText, flagsText = fields[0], fields[6], fields[3]
		}
		if excludedInterface(name) || name != filepath.Base(name) || name == "." || name == ".." {
			continue
		}
		flags, err := strconv.ParseUint(flagsText, 16, 64)
		if err != nil || flags&1 == 0 || flags&0x200 != 0 {
			continue
		}
		metric, err := strconv.ParseUint(metricText, base, 64)
		if err != nil || metric > best {
			continue
		}
		if metric < best {
			names = nil
			best = metric
		}
		names = append(names, name)
	}
	return names
}
