# OS Tuning - Disable background mutation, apply baseline sysctl/limits

Provisioned nodes ship with Ubuntu's stock defaults — `unattended-upgrades`,
apt timers, `needrestart` auto-restart, `snapd`, `motd-news`, `fwupd-refresh`.
All of these can mutate a host independently of our orchestration. The
`node_tuning` role disables them at provisioning time so the only way a
node ever changes is through `frameworks cluster provision` or
`frameworks cluster os update`.

## Posture

> **Production nodes do not install OS updates in the background.** All
> package installs and upgrades happen during provisioning or via an
> operator-triggered `frameworks cluster os update` command. Ubuntu's apt
> timers, `needrestart` auto-restart, and `snapd` are disabled at
> provisioning time so they cannot independently choose when services
> restart or hosts reboot.

## Architecture

```
provision flow                                update flow
──────────────                                ───────────
ensureNodeBaseline   (installs OS packages)
        │
        ▼
ensureNodeTuning     (this role)             frameworks cluster os update --check
        │                                              │
        ├─► apt policy   (disable u-u, timers)         ▼
        ├─► needrestart  (list-only mode)              read-only inventory
        ├─► snapd        (purge + mask)                of pending updates
        ├─► systemd      (mask motd-news, fwupd,
        │                 e2scrub if no LVM)
        ├─► sysctl       (10-frameworks-baseline)
        │   + edge addition (20-frameworks-edge)
        └─► limits       (10-frameworks-baseline)
                                                       │
                                                       ▼
                                              frameworks cluster os update --apply
                                                       │
                                                       ▼
                                              per-host (serial=1 by default):
                                                lock → apt update → apt upgrade
                                                → needrestart -r l
                                                → systemctl try-restart
                                                → reboot if required
                                                → verify clean
```

## What gets disabled

| Mechanism                                      | Default Ubuntu                        | After node_tuning           |
| ---------------------------------------------- | ------------------------------------- | --------------------------- |
| `APT::Periodic::Update-Package-Lists`          | 1                                     | 0                           |
| `APT::Periodic::Unattended-Upgrade`            | 1                                     | 0                           |
| `APT::Periodic::Download-Upgradeable-Packages` | 0 or 1                                | 0                           |
| `Unattended-Upgrade::Automatic-Reboot`         | false                                 | false (explicit)            |
| `apt-daily.timer`                              | enabled                               | masked                      |
| `apt-daily-upgrade.timer`                      | enabled                               | masked                      |
| `needrestart` restart mode                     | `i` (22.04) / `u` Ubuntu-mode (24.04) | `l` (list only)             |
| `needrestart` kernel/microcode prompts         | enabled                               | silenced                    |
| `snapd`                                        | installed + active                    | purged + masked             |
| `motd-news.timer`                              | enabled                               | masked                      |
| `fwupd-refresh.timer`                          | enabled                               | masked                      |
| `e2scrub_all.timer`                            | enabled                               | masked when no LVM detected |

## Sysctl baseline (core profile)

| Key                            | Value       | Why                                              |
| ------------------------------ | ----------- | ------------------------------------------------ |
| `net.core.somaxconn`           | 16384       | Default 4096 is small for any server workload.   |
| `net.core.netdev_max_backlog`  | 16384       | Bursty NIC handling.                             |
| `net.ipv4.tcp_max_syn_backlog` | 8192        | SYN flood resilience + listen() depth.           |
| `fs.file-max`                  | 2097152     | System-wide fd ceiling.                          |
| `kernel.pid_max`               | 4194304     | Linux max; allows container-heavy hosts.         |
| `vm.swappiness`                | 10          | Reduce thrashing on memory-tight DB/cache hosts. |
| `net.ipv4.ip_local_port_range` | 10000-65535 | Wider ephemeral range.                           |
| `net.core.rmem_max`            | 67108864    | Larger socket receive buffer cap.                |
| `net.core.wmem_max`            | 67108864    | Larger socket send buffer cap.                   |

## Sysctl additions (edge profile)

| Key                               | Value | Why                                                             |
| --------------------------------- | ----- | --------------------------------------------------------------- |
| `net.ipv4.tcp_notsent_lowat`      | 16384 | Tighter TCP write coalescing for HLS/WebRTC.                    |
| `net.core.default_qdisc`          | fq    | Paced, fair queueing — required for BBR.                        |
| `net.ipv4.tcp_congestion_control` | bbr   | Better-than-cubic over WAN; viewer-experience win on long-haul. |

## Limits baseline

```
*       soft    nofile      1048576
*       hard    nofile      1048576
*       soft    nproc       65535
*       hard    nproc       65535
root    soft    nofile      1048576
root    hard    nofile      1048576
```

PAM limits apply only to new login sessions. The verify task confirms the
file content (and mode) but does not assert `ulimit -n` at runtime —
Ansible's privilege escalation does not establish a PAM session, so an
in-Ansible `ulimit -n` check is not faithful.

## Key Files

- `ansible/collections/ansible_collections/frameworks/infra/roles/node_tuning/` - The role itself
- `ansible/playbooks/node_tuning.yml` - One-host playbook (used by edge standalone)
- `ansible/playbooks/cluster_os_update.yml` - Mutating upgrade + restart + reboot
- `cli/cmd/cluster_os_update.go` - `frameworks cluster os update [--check|--apply]`
- `cli/cmd/cluster_provision.go` (`ensureNodeTuning`) - Wires the role after `ensureNodeBaseline`
- `cli/pkg/provisioner/node_tuning.go` - Provisioner wrapping the role
- `cli/pkg/provisioner/node_tuning_role.go` - One-host helper used by `EdgeProvisioner`
- `docs/rfcs/cluster-os-update-drain.md` - Drain integration sequencing (follow-up)

## Cross-version contract

Targets Debian-family (Ubuntu 22.04, 24.04, 26.04 when released, Debian
bookworm). Achieved by:

- Every task gates on `ansible_facts.os_family == "Debian"`, never on
  `distribution_release`.
- All managed files are drop-ins under `.d/` directories: `/etc/apt/apt.conf.d/`,
  `/etc/needrestart/conf.d/`, `/etc/sysctl.d/`, `/etc/security/limits.d/`.
  Lex-sorted; our `zz-`-prefixed names always win the sort race.
- `systemctl mask` for timer suppression — survives package reinstall and
  doesn't depend on a specific systemd version.

## Carve-out: YugabyteDB

The `yugabyte` role keeps its own DB-specific sysctl + limits
(`roles/yugabyte/templates/sysctl.conf.j2`, `limits.conf.j2`,
`tasks/configure.yml`). These set `vm.swappiness=0` (vs node_tuning's
`10`), `vm.max_map_count=262144`, disable Transparent Huge Pages, and
write yugabyte-user-scoped `nofile=1048576 + nproc=12000`. These are
database-tuning values keyed to YugabyteDB's specific behavior, not
general OS tuning — they intentionally live with the role that owns them
and override the node_tuning baseline on yugabyte hosts.

## Gotchas

- **`needrestart` controls needrestart, not maintainer scripts.** Some
  packages restart their own services in `postinst`. We accept that
  limit; the primary defense is "no background upgrades", and
  operator-driven upgrades happen in a maintenance window where restarts
  are expected.
- **`/var/run/reboot-required` comes from `update-notifier-common`.**
  `node_tuning` installs that package so kernel/security updates expose the
  reboot signal consumed by `cluster os update`.
- **Edge profile applies BBR system-wide.** If you mix non-video TCP
  workloads on an edge node, BBR may behave differently than CUBIC
  — this is acceptable for our edge topology (edge nodes do video only)
  but worth knowing.
- **The lock file at `/var/lib/frameworks/locks/os_update.lock` is left
  behind if the SSH session drops mid-`cluster os update`.** The next run
  fails closed and prints the lock owner. Remove it only after checking that
  no update run is still active on the host.
- **`cluster os update --check` will not flag updates without `--refresh`
  if apt lists are stale.** Document operator practice: run with
  `--refresh` weekly, without it for quick spot-checks.
