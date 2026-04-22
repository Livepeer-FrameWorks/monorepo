package ansible

import (
	"sort"
	"strings"
)

// SystemdUnitSpec describes a systemd unit for RenderSystemdUnit to serialize.
// For a template unit (foo@.service), callers include %i in ExecStart etc and
// write the rendered string to /etc/systemd/system/foo@.service.
type SystemdUnitSpec struct {
	Description     string
	After           []string
	Wants           []string
	Requires        []string
	Type            string // "simple" (default), "forking", "oneshot", "notify"
	User            string
	Group           string
	Environment     map[string]string
	EnvironmentFile string
	ExecStart       string
	ExecStop        string
	ExecReload      string
	Restart         string // "always", "on-failure", "no"
	RestartSec      int
	LimitNOFILE     string
	LimitNPROC      string
	LimitCORE       string
	WorkingDir      string
	WantedBy        string // default "multi-user.target"
}

// RenderSystemdUnit serializes spec to a systemd unit file string.
func RenderSystemdUnit(spec SystemdUnitSpec) string {
	var b strings.Builder

	b.WriteString("[Unit]\n")
	if spec.Description != "" {
		b.WriteString("Description=")
		b.WriteString(spec.Description)
		b.WriteByte('\n')
	}
	if len(spec.After) > 0 {
		b.WriteString("After=")
		b.WriteString(strings.Join(spec.After, " "))
		b.WriteByte('\n')
	}
	if len(spec.Wants) > 0 {
		b.WriteString("Wants=")
		b.WriteString(strings.Join(spec.Wants, " "))
		b.WriteByte('\n')
	}
	if len(spec.Requires) > 0 {
		b.WriteString("Requires=")
		b.WriteString(strings.Join(spec.Requires, " "))
		b.WriteByte('\n')
	}

	b.WriteString("\n[Service]\n")
	svcType := spec.Type
	if svcType == "" {
		svcType = "simple"
	}
	b.WriteString("Type=")
	b.WriteString(svcType)
	b.WriteByte('\n')
	if spec.User != "" {
		b.WriteString("User=")
		b.WriteString(spec.User)
		b.WriteByte('\n')
	}
	if spec.Group != "" {
		b.WriteString("Group=")
		b.WriteString(spec.Group)
		b.WriteByte('\n')
	}
	if spec.WorkingDir != "" {
		b.WriteString("WorkingDirectory=")
		b.WriteString(spec.WorkingDir)
		b.WriteByte('\n')
	}
	if spec.EnvironmentFile != "" {
		b.WriteString("EnvironmentFile=")
		b.WriteString(spec.EnvironmentFile)
		b.WriteByte('\n')
	}
	// Emit Environment= lines in sorted key order for stable output.
	if len(spec.Environment) > 0 {
		keys := make([]string, 0, len(spec.Environment))
		for k := range spec.Environment {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("Environment=")
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(spec.Environment[k])
			b.WriteByte('\n')
		}
	}
	if spec.ExecStart != "" {
		b.WriteString("ExecStart=")
		b.WriteString(spec.ExecStart)
		b.WriteByte('\n')
	}
	if spec.ExecStop != "" {
		b.WriteString("ExecStop=")
		b.WriteString(spec.ExecStop)
		b.WriteByte('\n')
	}
	if spec.ExecReload != "" {
		b.WriteString("ExecReload=")
		b.WriteString(spec.ExecReload)
		b.WriteByte('\n')
	}
	if spec.Restart != "" {
		b.WriteString("Restart=")
		b.WriteString(spec.Restart)
		b.WriteByte('\n')
	}
	if spec.RestartSec > 0 {
		b.WriteString("RestartSec=")
		b.WriteString(itoa(spec.RestartSec))
		b.WriteByte('\n')
	}
	if spec.LimitNOFILE != "" {
		b.WriteString("LimitNOFILE=")
		b.WriteString(spec.LimitNOFILE)
		b.WriteByte('\n')
	}
	if spec.LimitNPROC != "" {
		b.WriteString("LimitNPROC=")
		b.WriteString(spec.LimitNPROC)
		b.WriteByte('\n')
	}
	if spec.LimitCORE != "" {
		b.WriteString("LimitCORE=")
		b.WriteString(spec.LimitCORE)
		b.WriteByte('\n')
	}

	b.WriteString("\n[Install]\n")
	wantedBy := spec.WantedBy
	if wantedBy == "" {
		wantedBy = "multi-user.target"
	}
	b.WriteString("WantedBy=")
	b.WriteString(wantedBy)
	b.WriteByte('\n')

	return b.String()
}

func itoa(n int) string {
	// Inline small-int formatter avoids pulling in strconv twice for one use.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
