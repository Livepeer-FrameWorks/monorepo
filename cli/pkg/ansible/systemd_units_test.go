package ansible

import (
	"strings"
	"testing"
)

func TestRenderSystemdUnit_minimumUnit(t *testing.T) {
	t.Parallel()
	got := RenderSystemdUnit(SystemdUnitSpec{
		Description: "Test Service",
		ExecStart:   "/usr/bin/true",
	})
	want := []string{
		"[Unit]",
		"Description=Test Service",
		"[Service]",
		"Type=simple",
		"ExecStart=/usr/bin/true",
		"[Install]",
		"WantedBy=multi-user.target",
	}
	for _, fragment := range want {
		if !strings.Contains(got, fragment) {
			t.Errorf("missing %q in:\n%s", fragment, got)
		}
	}
}

func TestRenderSystemdUnit_kafkaLike(t *testing.T) {
	t.Parallel()
	got := RenderSystemdUnit(SystemdUnitSpec{
		Description: "FrameWorks Kafka Broker",
		After:       []string{"network-online.target"},
		Wants:       []string{"network-online.target"},
		User:        "kafka",
		Group:       "kafka",
		ExecStart:   "/opt/kafka/bin/kafka-server-start.sh /etc/kafka/server.properties",
		ExecStop:    "/opt/kafka/bin/kafka-server-stop.sh",
		Restart:     "always",
		RestartSec:  5,
		LimitNOFILE: "100000",
	})
	want := []string{
		"Description=FrameWorks Kafka Broker",
		"After=network-online.target",
		"Wants=network-online.target",
		"User=kafka",
		"Group=kafka",
		"ExecStart=/opt/kafka/bin/kafka-server-start.sh /etc/kafka/server.properties",
		"ExecStop=/opt/kafka/bin/kafka-server-stop.sh",
		"Restart=always",
		"RestartSec=5",
		"LimitNOFILE=100000",
	}
	for _, fragment := range want {
		if !strings.Contains(got, fragment) {
			t.Errorf("missing %q in:\n%s", fragment, got)
		}
	}
}

func TestRenderSystemdUnit_templateUnitWithInstanceSpecifier(t *testing.T) {
	t.Parallel()
	got := RenderSystemdUnit(SystemdUnitSpec{
		Description: "Kafka %i",
		ExecStart:   "/opt/kafka/bin/kafka-server-start.sh /etc/kafka/%i.properties",
	})
	if !strings.Contains(got, "Description=Kafka %i") {
		t.Error("template unit must preserve %i in Description")
	}
	if !strings.Contains(got, "ExecStart=/opt/kafka/bin/kafka-server-start.sh /etc/kafka/%i.properties") {
		t.Error("template unit must preserve %i in ExecStart")
	}
}

func TestRenderSystemdUnit_environmentIsSorted(t *testing.T) {
	t.Parallel()
	got := RenderSystemdUnit(SystemdUnitSpec{
		Description: "Test",
		ExecStart:   "/usr/bin/true",
		Environment: map[string]string{
			"BAR": "2",
			"AAA": "1",
			"ZZZ": "3",
		},
	})
	// sorted order: AAA then BAR then ZZZ
	idxA := strings.Index(got, "Environment=AAA=1")
	idxB := strings.Index(got, "Environment=BAR=2")
	idxZ := strings.Index(got, "Environment=ZZZ=3")
	if idxA < 0 || idxB < 0 || idxZ < 0 {
		t.Fatalf("environment lines missing: %s", got)
	}
	if idxA >= idxB || idxB >= idxZ {
		t.Errorf("environment must be sorted by key: got order %d,%d,%d", idxA, idxB, idxZ)
	}
}

func TestRenderSystemdUnit_customWantedBy(t *testing.T) {
	t.Parallel()
	got := RenderSystemdUnit(SystemdUnitSpec{
		Description: "Boot-time service",
		ExecStart:   "/usr/bin/true",
		WantedBy:    "sysinit.target",
	})
	if !strings.Contains(got, "WantedBy=sysinit.target") {
		t.Error("custom WantedBy must override default")
	}
	if strings.Contains(got, "WantedBy=multi-user.target") {
		t.Error("default WantedBy must not be emitted when custom is set")
	}
}

func TestRenderSystemdUnit_defaultsTypeToSimple(t *testing.T) {
	t.Parallel()
	got := RenderSystemdUnit(SystemdUnitSpec{ExecStart: "/usr/bin/true"})
	if !strings.Contains(got, "Type=simple") {
		t.Error("Type must default to simple")
	}
}
