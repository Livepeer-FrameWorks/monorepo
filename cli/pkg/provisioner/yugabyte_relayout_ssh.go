package provisioner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	fwssh "frameworks/cli/pkg/ssh"
)

// SSHYugabyteNode runs relayout SQL and scripts on one tserver over an SSH runner, connecting to the node's local
// YSQL endpoint as the yugabyte superuser that the generated HBA trusts from the loopback address.
type SSHYugabyteNode struct {
	NodeName string
	Runner   fwssh.Runner
	// Host is the local YSQL address; production nodes use 127.0.0.1.
	Host string
	Port int
	// ApplicationName tags every connection; set it to RelayoutApplicationName of the invocation's lease owner so a
	// later invocation can end this one's remote work. Empty means the bare relayout prefix.
	ApplicationName string
}

func (n *SSHYugabyteNode) Name() string { return n.NodeName }

// Query writes the SQL to a temporary file through a randomly delimited heredoc so it never passes through shell
// quoting, then runs it with psql meta-commands enabled.
func (n *SSHYugabyteNode) Query(ctx context.Context, database, sql string) (string, error) {
	delimiter, err := relayoutRandomToken("FW_RELAYOUT_SQL_", 12)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(sql, "\n") {
		sql += "\n"
	}
	return n.Shell(ctx, fmt.Sprintf(`set -eu
%s
sql_file="$(mktemp)"
trap 'rm -f "$sql_file"' EXIT
cat > "$sql_file" <<'%s'
%s%s
PGAPPNAME=%s "$(fw_yb_bin ysqlsh)" -X -h %s -p %d -U yugabyte -d %s -v ON_ERROR_STOP=1 -q -tA -f "$sql_file"
`, YugabyteBinaryResolverShell, delimiter, sql, delimiter, shellQuote(n.applicationName()), shellQuote(n.host()), n.Port, shellQuote(database)))
}

// Shell runs script on the node and returns its stdout; a non-zero exit is an error carrying stderr. With an
// ApplicationName set, the script registers its process group as a relayout worker for as long as it runs.
func (n *SSHYugabyteNode) Shell(ctx context.Context, script string) (string, error) {
	if n.ApplicationName != "" {
		wrapped, err := relayoutWorkerScript(n.ApplicationName, script)
		if err != nil {
			return "", err
		}
		script = wrapped
	}
	result, err := n.Runner.RunScript(ctx, script)
	if err != nil {
		return "", fmt.Errorf("%s: %w", n.NodeName, err)
	}
	if result == nil {
		return "", fmt.Errorf("%s: script returned no result", n.NodeName)
	}
	if result.ExitCode != 0 {
		return result.Stdout, fmt.Errorf("%s: exit %d: %s", n.NodeName, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return result.Stdout, nil
}

func (n *SSHYugabyteNode) applicationName() string {
	if n.ApplicationName == "" {
		return relayoutApplicationName
	}
	return n.ApplicationName
}

func (n *SSHYugabyteNode) host() string {
	if n.Host == "" {
		return "127.0.0.1"
	}
	return n.Host
}

func relayoutRandomToken(prefix string, size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return prefix + hex.EncodeToString(buf), nil
}

// relayoutWorkerRoot holds one directory per relayout application name on each host. A script a relayout runs there
// records its process group in that directory for as long as it runs, because a script keeps running on the host after
// the SSH client that started it dies: without a terminal, the remote side gets no hangup.
//
// Admission and revocation follow one protocol. A worker publishes its complete record atomically (write a hidden
// temporary file, then rename it into place) and only afterwards checks for the directory's revoked marker, removing
// its record and exiting before its body runs when the marker exists. A fence creates the marker first and only
// afterwards scans and stops recorded groups. Whatever the interleaving, a worker either finds the marker and never
// runs its body, or published its record before the scan began and is stopped by it.
const relayoutWorkerRoot = "/var/lib/frameworks/relayout-workers"

// relayoutTrustedDirShell defines fw_trusted, which succeeds only when its argument and every directory above it is a
// real directory owned by root or the current user and writable by neither group nor others. A registry anyone else
// could create or write would let them forge or remove records, so that the fence signals unrelated process groups
// or misses real workers; such a path is refused rather than trusted.
const relayoutTrustedDirShell = `fw_trusted() {
  fw_d="$1"
  fw_me="$(id -u)" || return 1
  while :; do
    if [ -L "$fw_d" ] || [ ! -d "$fw_d" ]; then return 1; fi
    fw_owner="$(stat -c %u "$fw_d")" || return 1
    fw_mode="$(stat -c %a "$fw_d")" || return 1
    [ "$fw_owner" = 0 ] || [ "$fw_owner" = "$fw_me" ] || return 1
    [ $((0$fw_mode & 022)) -eq 0 ] || return 1
    [ "$fw_d" = / ] && return 0
    fw_d="$(dirname "$fw_d")"
  done
}`

// relayoutWorkerRevoked is the marker a fence creates in an application's worker directory.
const relayoutWorkerRevoked = "revoked"

// Exit statuses of a worker that did not run its body.
const (
	relayoutWorkerUnregistered = 97
	relayoutWorkerRevokedExit  = 98
)

// relayoutWorkerScript wraps body so it runs only as an admitted worker of application. Every registration step must
// succeed, the process group and its leader's start time must be valid, and the record must be published and read
// back before the revoked marker is checked; any failure exits before body runs. FW_RELAYOUT_WORKER names the record.
func relayoutWorkerScript(application, body string) (string, error) {
	delimiter, err := relayoutRandomToken("FW_RELAYOUT_WORKER_", 12)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return fmt.Sprintf(`set -eu
fw_worker_fail() { echo "relayout worker not admitted: $1" >&2; exit %[4]d; }
%[7]s
fw_worker_dir=%[1]s
(umask 022; mkdir -p "$(dirname "$fw_worker_dir")") || fw_worker_fail "cannot create the parent of $fw_worker_dir"
mkdir -p -m 0700 "$fw_worker_dir" || fw_worker_fail "cannot create $fw_worker_dir"
fw_trusted "$fw_worker_dir" || fw_worker_fail "$fw_worker_dir or a directory above it can be written by someone other than root or $(id -un)"
fw_worker_pgid="$(sed 's/^.*) //' /proc/$$/stat | awk '{print $3}')" || fw_worker_fail "cannot read the process group"
case "$fw_worker_pgid" in ''|*[!0-9]*) fw_worker_fail "process group '$fw_worker_pgid' is not a number" ;; esac
[ "$fw_worker_pgid" -gt 1 ] || fw_worker_fail "process group $fw_worker_pgid cannot be fenced"
fw_worker_start="$(sed 's/^.*) //' "/proc/$fw_worker_pgid/stat" | awk '{print $20}')" || fw_worker_fail "cannot read the group leader"
case "$fw_worker_start" in ''|*[!0-9]*) fw_worker_fail "group leader start time '$fw_worker_start' is not a number" ;; esac
FW_RELAYOUT_WORKER="$fw_worker_dir/$$"
fw_worker_tmp="$fw_worker_dir/.$$.tmp"
printf '%%s %%s\n' "$fw_worker_pgid" "$fw_worker_start" > "$fw_worker_tmp" || fw_worker_fail "cannot write the worker record"
mv -f "$fw_worker_tmp" "$FW_RELAYOUT_WORKER" || fw_worker_fail "cannot publish the worker record"
[ "$(cat "$FW_RELAYOUT_WORKER")" = "$fw_worker_pgid $fw_worker_start" ] || fw_worker_fail "the published worker record does not read back"
if [ -e "$fw_worker_dir/%[5]s" ]; then
  rm -f "$FW_RELAYOUT_WORKER"
  echo "relayout worker not admitted: its owner was revoked" >&2
  exit %[6]d
fi
export FW_RELAYOUT_WORKER
set +e
sh <<'%[2]s'
%[3]s%[2]s
fw_worker_status=$?
rm -f "$FW_RELAYOUT_WORKER"
exit "$fw_worker_status"
`, shellQuote(relayoutWorkerRoot+"/"+application), delimiter, body, relayoutWorkerUnregistered, relayoutWorkerRevoked, relayoutWorkerRevokedExit, relayoutTrustedDirShell), nil
}

// relayoutStopWorkersScript revokes application on this host and stops every process group recorded under it, then
// waits for them to exit: TERM three times, then KILL. The revoked marker is created before the scan, and the script
// fails when it cannot be. A record whose leader process id now belongs to another process, or whose group has no
// process left, is stale and removed. It never signals group 1 or its own group, and prints remaining=<count>.
func relayoutStopWorkersScript(application string) string {
	return fmt.Sprintf(`set -u
%[3]s
dir=%[1]s
(umask 022; mkdir -p "$(dirname "$dir")") || { echo "cannot create the parent of $dir" >&2; exit 1; }
mkdir -p -m 0700 "$dir" || { echo "cannot create $dir" >&2; exit 1; }
fw_trusted "$dir" || { echo "$dir or a directory above it can be written by someone other than root or $(id -un); refusing to trust its records" >&2; exit 1; }
: > "$dir/%[2]s" || { echo "cannot revoke $dir" >&2; exit 1; }
[ -e "$dir/%[2]s" ] || { echo "revoked marker in $dir did not persist" >&2; exit 1; }
own_pgid="$(sed 's/^.*) //' /proc/$$/stat | awk '{print $3}')"
groups_running() { cat /proc/[0-9]*/stat 2>/dev/null | sed 's/^.*) //' | awk '{print $3}' | sort -u; }
live() {
  running="$(groups_running)"
  for f in "$dir"/[0-9]*; do
    [ -f "$f" ] || continue
    pgid=""; start=""
    read -r pgid start < "$f" || true
    case "$pgid" in ''|*[!0-9]*) rm -f "$f"; continue ;; esac
    if [ "$pgid" -le 1 ] || [ "$pgid" = "$own_pgid" ]; then continue; fi
    if [ -e "/proc/$pgid" ] && [ "$(sed 's/^.*) //' "/proc/$pgid/stat" 2>/dev/null | awk '{print $20}')" != "$start" ]; then rm -f "$f"; continue; fi
    if printf '%%s\n' "$running" | grep -qx "$pgid"; then echo "$pgid"; else rm -f "$f"; fi
  done
}
for sig in TERM TERM TERM KILL; do
  groups="$(live)"
  [ -z "$groups" ] && break
  for g in $groups; do kill -s "$sig" -- "-$g" 2>/dev/null || true; done
  i=0
  while [ -n "$(live)" ] && [ "$i" -lt 10 ]; do sleep 1; i=$((i+1)); done
done
remaining="$(live | wc -l | tr -d ' ')"
echo "remaining=$remaining"
`, shellQuote(relayoutWorkerRoot+"/"+application), relayoutWorkerRevoked, relayoutTrustedDirShell)
}

// WithApplicationName returns a copy of the node whose connections and scripts carry name.
func (n *SSHYugabyteNode) WithApplicationName(name string) YugabyteNode {
	copied := *n
	copied.ApplicationName = name
	return &copied
}
