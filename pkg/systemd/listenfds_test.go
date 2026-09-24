package systemd

import "testing"

func TestListenFDCountRequiresMatchingPID(t *testing.T) {
	cases := []struct {
		name, pid, fds string
		want           int
	}{
		{"not activated", "", "", 0},
		{"matching pid", "42", "2", 2},
		{"other process", "7", "2", 0},
		{"garbage count", "42", "x", 0},
		{"negative count", "42", "-1", 0},
	}
	for _, tc := range cases {
		if got := listenFDCount(tc.pid, tc.fds, 42); got != tc.want {
			t.Errorf("%s: listenFDCount = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestListenFDsIgnoresAnotherProcessesDescriptors(t *testing.T) {
	t.Setenv("LISTEN_PID", "1")
	t.Setenv("LISTEN_FDS", "2")
	if files := ListenFDs(); files != nil {
		t.Fatalf("descriptors for another process were adopted: %v", files)
	}
}
