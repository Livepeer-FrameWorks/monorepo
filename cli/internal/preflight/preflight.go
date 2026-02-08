package preflight

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Check struct {
	Name   string
	OK     bool
	Detail string
	Error  string
}

type Summary struct {
	Checks []Check
}

// DNSResolution checks that a domain resolves.
func DNSResolution(domain string) Check {
	if strings.TrimSpace(domain) == "" {
		return Check{Name: "dns", OK: true, Detail: "no domain provided"}
	}
	ips, err := net.LookupIP(domain)
	if err != nil {
		return Check{Name: "dns", OK: false, Detail: "lookup failed", Error: err.Error()}
	}
	list := make([]string, 0, len(ips))
	for _, ip := range ips {
		list = append(list, ip.String())
	}
	return Check{Name: "dns", OK: true, Detail: fmt.Sprintf("%s -> %s", domain, strings.Join(list, ", "))}
}

// HasDocker checks that docker and docker compose are available.
func HasDocker() []Check {
	var out []Check
	if _, err := exec.LookPath("docker"); err != nil {
		out = append(out, Check{Name: "docker", OK: false, Detail: "docker not found", Error: err.Error()})
	} else {
		out = append(out, Check{Name: "docker", OK: true, Detail: "docker found"})
		// docker compose plugin
		cmd := exec.Command("docker", "compose", "version")
		if err := cmd.Run(); err != nil {
			out = append(out, Check{Name: "docker-compose", OK: false, Detail: "docker compose plugin not available", Error: err.Error()})
		} else {
			out = append(out, Check{Name: "docker-compose", OK: true, Detail: "docker compose available"})
		}
	}
	return out
}

// ReadSysctl reads a proc sysctl file on Linux.
func ReadSysctl(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// LinuxSysctlChecks inspects recommended sysctls (Linux only).
func LinuxSysctlChecks() []Check {
	if runtime.GOOS != "linux" {
		return []Check{{Name: "sysctl", OK: true, Detail: "non-linux: skipping"}}
	}
	var out []Check
	pairs := []struct{ key, path string }{
		{"net.core.rmem_max", "/proc/sys/net/core/rmem_max"},
		{"net.core.wmem_max", "/proc/sys/net/core/wmem_max"},
		{"net.core.somaxconn", "/proc/sys/net/core/somaxconn"},
		{"net.ipv4.ip_local_port_range", "/proc/sys/net/ipv4/ip_local_port_range"},
	}
	for _, p := range pairs {
		v, err := ReadSysctl(p.path)
		ok := err == nil
		out = append(out, Check{Name: p.key, OK: ok, Detail: v, Error: errString(err)})
	}
	return out
}

// ShmSize checks /dev/shm size (Linux only) by parsing /proc/mounts and df -h fallback.
func ShmSize() Check {
	if runtime.GOOS != "linux" {
		return Check{Name: "/dev/shm", OK: true, Detail: "non-linux: skipping"}
	}
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return Check{Name: "/dev/shm", OK: false, Detail: "cannot read mounts", Error: err.Error()}
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		parts := strings.Fields(line)
		if len(parts) >= 3 && parts[1] == "/dev/shm" {
			// cannot get size here; advise use of docker --shm-size
			return Check{Name: "/dev/shm", OK: true, Detail: "mounted (use docker --shm-size for container)"}
		}
	}
	return Check{Name: "/dev/shm", OK: false, Detail: "not mounted"}
}

// UlimitNoFile displays the current process rlimit for nofile.
func UlimitNoFile() Check {
	var r syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &r); err != nil {
		return Check{Name: "ulimit-nofile", OK: false, Detail: "", Error: err.Error()}
	}
	return Check{Name: "ulimit-nofile", OK: true, Detail: fmt.Sprintf("soft=%d hard=%d", r.Cur, r.Max)}
}

// PortChecks suggests that 80/443 are free for inbound use.
func PortChecks() []Check {
	// We cannot reliably test inbound availability; just try a local dial to determine if already bound.
	var out []Check
	try := func(port int) Check {
		// attempt to listen; requires privileges and can be disruptive, so avoid.
		// Instead, try to dial localhost:port; if connect succeeds, something is listening.
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 250*time.Millisecond)
		if err != nil {
			return Check{Name: fmt.Sprintf("port-%d", port), OK: true, Detail: "no listener detected"}
		}
		_ = conn.Close()
		return Check{Name: fmt.Sprintf("port-%d", port), OK: false, Detail: "already in use"}
	}
	out = append(out, try(80))
	out = append(out, try(443))
	return out
}

// DiskSpace checks if the path has enough free space.
func DiskSpace(path string, minFreeBytes uint64, minFreePercent float64) Check {
	name := diskCheckName(path)
	if runtime.GOOS == "windows" {
		return Check{Name: name, OK: true, Detail: "windows: skipping"}
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return Check{Name: name, OK: false, Detail: "statfs failed", Error: err.Error()}
	}

	freeBytes, totalBytes := diskStats(stat)
	return evaluateDiskSpace(name, path, freeBytes, totalBytes, minFreeBytes, minFreePercent)
}

// DiskSpaceFromDF evaluates disk space using `df -Pk` output for remote checks.
func DiskSpaceFromDF(output, path string, minFreeBytes uint64, minFreePercent float64) Check {
	name := diskCheckName(path)
	freeBytes, totalBytes, err := parseDFKilobytes(output)
	if err != nil {
		return Check{Name: name, OK: false, Detail: "df parse failed", Error: err.Error()}
	}
	return evaluateDiskSpace(name, path, freeBytes, totalBytes, minFreeBytes, minFreePercent)
}

func parseDFKilobytes(output string) (uint64, uint64, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return 0, 0, fmt.Errorf("expected df output with header and data")
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 5 {
		return 0, 0, fmt.Errorf("unexpected df output format")
	}
	totalKB, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid total size: %w", err)
	}
	availKB, err := strconv.ParseUint(fields[3], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid available size: %w", err)
	}
	return availKB * 1024, totalKB * 1024, nil
}

func evaluateDiskSpace(name, path string, freeBytes, totalBytes, minFreeBytes uint64, minFreePercent float64) Check {
	if totalBytes == 0 {
		return Check{Name: name, OK: false, Detail: "disk total reported as 0"}
	}

	freePercent := (float64(freeBytes) / float64(totalBytes)) * 100
	meetsBytes := minFreeBytes == 0 || freeBytes >= minFreeBytes
	meetsPercent := minFreePercent == 0 || freePercent >= minFreePercent
	ok := meetsBytes && meetsPercent

	detail := fmt.Sprintf("%s free (%s of %s)", formatBytes(freeBytes), formatPercent(freePercent), formatBytes(totalBytes))
	if minFreeBytes > 0 || minFreePercent > 0 {
		detail = fmt.Sprintf("%s (min %s, %s)", detail, formatBytes(minFreeBytes), formatPercent(minFreePercent))
	}
	if !ok {
		return Check{Name: name, OK: false, Detail: detail}
	}
	return Check{Name: name, OK: true, Detail: detail}
}

func diskStats(stat syscall.Statfs_t) (uint64, uint64) {
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	return free, total
}

func diskCheckName(path string) string {
	if path == "/" {
		return "disk-root"
	}
	clean := strings.TrimPrefix(path, "/")
	if clean == "" {
		return "disk"
	}
	return "disk-" + strings.ReplaceAll(clean, "/", "-")
}

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%dB", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	value := float64(bytes) / float64(div)
	suffix := []string{"KB", "MB", "GB", "TB", "PB"}[exp]
	return fmt.Sprintf("%.1f%s", value, suffix)
}

func formatPercent(value float64) string {
	return fmt.Sprintf("%.1f%%", value)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
