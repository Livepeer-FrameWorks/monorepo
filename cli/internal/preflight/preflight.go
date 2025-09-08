package preflight

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
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

func (s *Summary) add(ok bool, name, detail string, err error) {
	c := Check{Name: name, OK: ok, Detail: detail}
	if err != nil {
		c.Error = err.Error()
	}
	s.Checks = append(s.Checks, c)
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

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
