#!/usr/bin/env python3
"""Cause a kernel OOM of an owned, running media processor in an isolated edge."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys


def docker(*args, timeout=30):
    return subprocess.check_output(["docker", *args], text=True, timeout=timeout)


def force(container, stream, destination):
    project = os.environ.get("COMPOSE_PROJECT_NAME", "")
    assert os.environ.get("STACK_TARGET", "stack") != "staging", "isolated stack only"
    assert re.fullmatch(r"fw-stack-\d+", project), "missing isolated stack project"
    assert re.fullmatch(r"processing\+[A-Za-z0-9_-]{16,128}", stream), "invalid owned stream"
    owner = json.loads(docker("inspect", container))[0]
    assert owner["Config"]["Labels"].get("com.docker.compose.project") == project, "foreign edge"
    assert owner["HostConfig"]["Memory"] == 1024**3, "fixture must bound this edge to 1 GiB first"
    runner = docker("ps", "-q", "--filter", "label=com.docker.compose.project=" + project,
                    "--filter", "label=com.docker.compose.service=stack-runner").strip()
    assert runner and "\n" not in runner, "resolve this stack's runner image"
    image = docker("inspect", "--format", "{{.Image}}", runner).strip()
    probe = r'''
import json, pathlib, sys, time
def birth(path): return path.joinpath("stat").read_text().rsplit(")",1)[1].split()[19]
deadline=time.monotonic()+30
while time.monotonic()<deadline:
 candidates=[]; services={}
 for path in pathlib.Path("/proc").iterdir():
  if not path.name.isdigit(): continue
  try:
   name=path.joinpath("comm").read_text().strip()
   if name in ("MistController","helmsman"):
    services[path.name]={"comm":name,"birth":birth(path)}
   if name!="MistProcAV": continue
   own=False
   for argument in path.joinpath("cmdline").read_bytes().split(b"\0"):
    try: own=own or json.loads(argument).get("source")==sys.argv[1]
    except (ValueError,AttributeError,UnicodeError): pass
   if not own: continue
   status=dict(line.split(":",1) for line in path.joinpath("status").read_text().splitlines() if ":" in line)
   anonymous=int(status.get("RssAnon","0").split()[0])*1024
   if anonymous>=16*1024*1024:
    candidates.append({"pid":int(path.name),"birth":birth(path),"anonymous_bytes":anonymous})
  except (OSError,IndexError,ValueError): continue
 if candidates:
  selected=max(candidates,key=lambda row:row["anonymous_bytes"])
  time.sleep(1)
  path=pathlib.Path("/proc",str(selected["pid"]))
  try:
   if birth(path)!=selected["birth"]: continue
   print(json.dumps({"processor":selected,"services":services}));sys.exit(0)
  except OSError: continue
 time.sleep(.1)
raise SystemExit("no stable owned native AV processor became resident")
'''
    evidence = json.loads(docker("run", "--rm", "--network", "none", "--cpus", "1", "--memory", "128m",
                                 "--pid", "container:" + container, "--entrypoint", "python3", image,
                                 "-c", probe, stream, timeout=40))
    assert any(row["comm"] == "helmsman" for row in evidence["services"].values()), "sidecar identity missing"
    assert any(row["comm"] == "MistController" for row in evidence["services"].values()), "controller identity missing"
    processor = evidence["processor"]
    pressure = "/dev/shm/fw-oom-" + stream.split("+", 1)[1]
    state = "/tmp/fw-oom-" + stream.split("+", 1)[1] + ".owner"
    script = r'''
set -euo pipefail
pid=$1 birth=$2 pressure=$3 state=$4
current_birth() {
 local row fields
 IFS= read -r row < "/proc/$pid/stat" || return 1
 read -ra fields <<< "${row##*) }"
 current_stamp=${fields[19]:-}
}
kills() { awk '$1=="oom_kill"{print $2}' /sys/fs/cgroup/memory.events; }
current_birth && [ "$current_stamp" = "$birth" ] || exit 1
old_score=$(cat "/proc/$pid/oom_score_adj")
cleanup() {
 # Cleanup utilities must have memory available to fork.
 unset chunks chunk
 exec 9>&- || true
 rm -f "$pressure" "$state"
 if current_birth && [ "$current_stamp" = "$birth" ]; then
  printf '%s' "$old_score" > "/proc/$pid/oom_score_adj" || true
 fi
}
trap cleanup EXIT
before=$(kills)
owner_birth=$(awk '{print $22}' "/proc/$$/stat")
printf '%s %s %s %s 0\n' "$$" "$owner_birth" "$before" "$old_score" > "$state"
printf '1000' > "/proc/$pid/oom_score_adj"
# Forking while pressure is held can stall before the kernel reaches OOM.
# A FIFO read supplies the inter-allocation pause without spawning a child.
mkfifo "$pressure"
exec 9<> "$pressure"
chunks=()
printf -v chunk '%4194304s' ''
for ((block=0; block<256; block++)); do
 [ -r "/proc/$pid/stat" ] && current_birth && [ "$current_stamp" = "$birth" ] || exit 1
 printf '%s %s %s %s %s\n' "$$" "$owner_birth" "$before" "$old_score" "$((block+1))" > "$state"
 chunks+=("$chunk")
 # Let the observer stop pressure after a kill before another block is written.
 read -r -t .05 -u 9 unused || true
done
exit 1
'''
    # This observer has its own cgroup. Reclaim in the bounded edge must not
    # prevent it from stopping the pressure writer on the first kernel kill.
    watch = r'''
import json,os,pathlib,signal,sys,time
root=pathlib.Path("/proc/1/root"); state=root/sys.argv[1].lstrip("/")
pressure=root/sys.argv[2].lstrip("/");owner=None
def birth(pid):return pathlib.Path("/proc",str(pid),"stat").read_text().rsplit(")",1)[1].split()[19]
def kills():
 return int(dict(line.split() for line in (root/"sys/fs/cgroup/memory.events").read_text().splitlines())["oom_kill"])
def stop():
 if owner is not None:
  pid,stamp,_,_,_=owner
  try:
   if birth(pid)==stamp and os.getpgid(pid)==pid:os.killpg(pid,signal.SIGTERM)
  except ProcessLookupError:pass
  except FileNotFoundError:pass
 pressure.unlink(missing_ok=True)
deadline=time.monotonic()+35
try:
 while time.monotonic()<deadline:
  try:
   fields=state.read_text().split()
   if len(fields)==5:owner=(int(fields[0]),fields[1],int(fields[2]),int(fields[3]),int(fields[4]))
  except FileNotFoundError:pass
  if owner is not None:
   after=kills()
   if after>owner[2]:
    stop()
    time.sleep(.1)
    final=kills()
    assert final==owner[2]+1,("more than the single deliberate kernel kill",owner[2],final)
    print(json.dumps({"initial_oom_kills":owner[2],"final_oom_kills":final,"pressure_blocks_started":owner[4],"pressure_observer_outside_edge_cgroup":True}))
    break
  time.sleep(.01)
 else:
  details={"owner":owner,"observed_oom_kills":kills(),"pressure_exists":pressure.exists(),"state_exists":state.exists()}
  for name in ["memory.current","memory.max","memory.swap.current","memory.swap.max","memory.events"]:
   details[name]=(root/"sys/fs/cgroup"/name).read_text().strip()
  raise SystemExit("no kernel OOM within the bounded pressure window: "+json.dumps(details))
finally:stop()
'''
    worker = None
    try:
        worker = subprocess.Popen(["docker", "exec", "-i", container, "setsid", "--fork", "--wait", "bash", "-s", "--",
                                   str(processor["pid"]), processor["birth"], pressure, state],
                                  stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        worker.stdin.write(script)
        worker.stdin.close()
        worker.stdin = None
        observed = docker("run", "--rm", "--network", "none", "--cpus", "1", "--memory", "128m",
                          "--pid", "container:" + container, "--entrypoint", "python3", image,
                          "-c", watch, state, pressure, timeout=45)
        worker.communicate(timeout=10)
        evidence.update(json.loads(observed))
        evidence["pressure_kind"] = "retained_anonymous"
        evidence["pressure_block_bytes"] = 4 * 1024 * 1024
        check = r'''
import json,pathlib,sys,time
before=json.loads(sys.argv[1]);pid=sys.argv[2];stamp=sys.argv[3]
def birth(path): return path.joinpath("stat").read_text().rsplit(")",1)[1].split()[19]
for _ in range(50):
 path=pathlib.Path("/proc",pid)
 if not path.exists() or birth(path)!=stamp: break
 time.sleep(.1)
else: raise SystemExit("selected processor survived the claimed OOM")
for number,row in before.items():
 path=pathlib.Path("/proc",number)
 assert path.exists() and birth(path)==row["birth"],("service restarted during child OOM",number,row)
print("native processor killed; controller and sidecar stayed alive")
'''
        print(docker("run", "--rm", "--network", "none", "--cpus", "1", "--memory", "128m",
                     "--pid", "container:" + container, "--entrypoint", "python3", image, "-c", check,
                     json.dumps(evidence["services"]), str(processor["pid"]), processor["birth"]).strip())
        Path(destination).write_text(json.dumps(evidence, indent=2) + "\n")
    finally:
        if worker is not None:
            if worker.poll() is None:
                worker.terminate()
            output, errors = worker.communicate(timeout=10)
            print(json.dumps({"pressure_worker_exit":worker.returncode,"stdout":output,"stderr":errors}), file=sys.stderr)
        subprocess.run(["docker", "exec", container, "rm", "-f", pressure, state], capture_output=True, timeout=10)


if __name__ == "__main__":
    force(*sys.argv[1:])
