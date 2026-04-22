package provisioner

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/ansible"
)

var testTimesyncSpec = ansible.DistroPackageSpec{PackageName: "chrony", ServiceName: "chronyd"}

func TestYugabyteProvisionTasks_coreShape(t *testing.T) {
	t.Parallel()
	params := YugabyteNativeParams{
		MasterAddresses: "10.0.0.1:7100,10.0.0.2:7100,10.0.0.3:7100",
		NodeIP:          "10.0.0.1",
		DataDir:         "/var/lib/yugabyte/data",
		RF:              3,
		YSQLPort:        5433,
		Cloud:           "frameworks",
		Region:          "eu",
		Zone:            "eu-1",
	}
	tasks := yugabyteProvisionTasks(params,
		"https://downloads.yugabyte.com/releases/2025.1.3.2/yugabyte-2025.1.3.2-b1-linux-x86_64.tar.gz",
		"sha256:31d8b3a65f75d96a3ffe9fe61f8398018576afe8439c0b43115075296cee3a20",
		testTimesyncSpec)

	// Must contain these module invocations in the rendered task list.
	wantModules := []string{
		"ansible.builtin.group",
		"ansible.builtin.user",
		"ansible.builtin.copy",
		"ansible.builtin.shell",
		"ansible.builtin.file",
		"ansible.builtin.get_url",
		"ansible.builtin.unarchive",
		"ansible.builtin.systemd_service",
	}
	for _, want := range wantModules {
		found := false
		for _, task := range tasks {
			if task.Module == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tasks missing module %q", want)
		}
	}
}

func TestYugabyteProvisionTasks_usesPinnedArtifact(t *testing.T) {
	t.Parallel()
	wantURL := "https://downloads.yugabyte.com/releases/2025.1.3.2/yugabyte-2025.1.3.2-b1-linux-x86_64.tar.gz"
	wantSum := "sha256:deadbeef"
	tasks := yugabyteProvisionTasks(YugabyteNativeParams{}, wantURL, wantSum, testTimesyncSpec)

	var getURL map[string]any
	for _, task := range tasks {
		if task.Module == "ansible.builtin.get_url" {
			getURL = task.Args
			break
		}
	}
	if getURL == nil {
		t.Fatal("no get_url task emitted")
	}
	if getURL["url"] != wantURL {
		t.Errorf("get_url.url = %v, want %v", getURL["url"], wantURL)
	}
	if getURL["checksum"] != wantSum {
		t.Errorf("get_url.checksum = %v, want %v", getURL["checksum"], wantSum)
	}
}

func TestYugabyteProvisionTasks_unarchiveStripsOneComponent(t *testing.T) {
	t.Parallel()
	tasks := yugabyteProvisionTasks(YugabyteNativeParams{}, "https://x/a.tgz", "sha256:aa", testTimesyncSpec)
	var unarchive map[string]any
	for _, task := range tasks {
		if task.Module == "ansible.builtin.unarchive" {
			unarchive = task.Args
			break
		}
	}
	if unarchive == nil {
		t.Fatal("no unarchive task emitted")
	}
	opts, ok := unarchive["extra_opts"].([]string)
	if !ok || len(opts) == 0 || opts[0] != "--strip-components=1" {
		t.Errorf("unarchive must strip one component; got extra_opts=%v", unarchive["extra_opts"])
	}
	// Must gate on a version-keyed install sentinel, NOT a stable path like
	// /opt/yugabyte/bin/yb-master — the stable path makes extract skip on
	// version bumps (upgrade-hostile).
	creates, _ := unarchive["creates"].(string)
	if !strings.HasPrefix(creates, "/opt/yugabyte/.installed-") {
		t.Errorf("unarchive creates: must be a /opt/yugabyte/.installed-* sentinel; got %q", creates)
	}
}

func TestYugabyteProvisionTasks_sentinelRotatesOnVersionBump(t *testing.T) {
	t.Parallel()
	oldTasks := yugabyteProvisionTasks(YugabyteNativeParams{},
		"https://downloads.yugabyte.com/releases/2025.1.3.2/yugabyte-2025.1.3.2-linux-x86_64.tar.gz",
		"sha256:aa", testTimesyncSpec)
	newTasks := yugabyteProvisionTasks(YugabyteNativeParams{},
		"https://downloads.yugabyte.com/releases/2025.2.0.0/yugabyte-2025.2.0.0-linux-x86_64.tar.gz",
		"sha256:bb", testTimesyncSpec)
	creates := func(tasks []ansible.Task) string {
		for _, t := range tasks {
			if t.Module == "ansible.builtin.unarchive" {
				if s, ok := t.Args["creates"].(string); ok {
					return s
				}
			}
		}
		return ""
	}
	if creates(oldTasks) == creates(newTasks) {
		t.Errorf("version bump must rotate unarchive creates sentinel; got %q for both",
			creates(oldTasks))
	}
}

func TestYugabyteProvisionTasks_systemdStartsMasterBeforeTServer(t *testing.T) {
	t.Parallel()
	tasks := yugabyteProvisionTasks(YugabyteNativeParams{}, "https://x/a.tgz", "sha256:aa", testTimesyncSpec)
	var masterIdx, tserverIdx = -1, -1
	for i, task := range tasks {
		if task.Module != "ansible.builtin.systemd_service" {
			continue
		}
		name, _ := task.Args["name"].(string)
		switch name {
		case "yb-master":
			masterIdx = i
		case "yb-tserver":
			tserverIdx = i
		}
	}
	if masterIdx < 0 || tserverIdx < 0 {
		t.Fatalf("expected both yb-master and yb-tserver systemd_service tasks; got master=%d tserver=%d", masterIdx, tserverIdx)
	}
	if masterIdx >= tserverIdx {
		t.Errorf("yb-master must be enabled before yb-tserver (master=%d, tserver=%d)", masterIdx, tserverIdx)
	}
}

func TestYugabyteProvisionTasks_configsMatchParams(t *testing.T) {
	t.Parallel()
	params := YugabyteNativeParams{
		MasterAddresses: "10.0.0.1:7100",
		NodeIP:          "10.0.0.1",
		DataDir:         "/var/lib/yugabyte/data",
		RF:              3,
		YSQLPort:        5433,
		Cloud:           "frameworks",
		Region:          "eu",
		Zone:            "eu-1",
	}
	tasks := yugabyteProvisionTasks(params, "https://x/a.tgz", "sha256:aa", testTimesyncSpec)
	var masterConf, tserverConf string
	for _, task := range tasks {
		if task.Module != "ansible.builtin.copy" {
			continue
		}
		dest, _ := task.Args["dest"].(string)
		content, _ := task.Args["content"].(string)
		switch dest {
		case "/opt/yugabyte/conf/master.conf":
			masterConf = content
		case "/opt/yugabyte/conf/tserver.conf":
			tserverConf = content
		}
	}
	if !strings.Contains(masterConf, "--master_addresses=10.0.0.1:7100") {
		t.Errorf("master.conf missing master_addresses; got:\n%s", masterConf)
	}
	if !strings.Contains(tserverConf, "--tserver_master_addrs=10.0.0.1:7100") {
		t.Errorf("tserver.conf missing tserver_master_addrs; got:\n%s", tserverConf)
	}
}
