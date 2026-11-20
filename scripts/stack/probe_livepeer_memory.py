#!/usr/bin/env python3
"""Exercise consecutive QHD/UHD sessions against an isolated CPU orchestrator."""
import email
import email.policy
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.request


def run(*args, check=True):
    result = subprocess.run(args, check=False, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    if check and result.returncode:
        raise RuntimeError(f"{args!r} exited {result.returncode}:\n{result.stdout}")
    return result


def main():
    image = os.environ["GO_LIVEPEER_PROBE_IMAGE"]
    memory = os.environ.get("LIVEPEER_PROBE_MEMORY", "2g")
    work = Path(os.environ.get("LIVEPEER_PROBE_DIR", f".stack/livepeer-memory-probe/{memory}-{os.getpid()}")).resolve()
    work.mkdir(parents=True, exist_ok=True)
    project = f"fw-orch-memory-{os.getuid()}-{os.getpid()}"
    compose = work / "compose.json"
    compose.write_text(json.dumps({"services": {
        "orch": {"image": image, "command": ["-orchestrator", "-transcoder", "-network=offchain", "-pricePerUnit=1500", "-pixelsPerUnit=1000", "-serviceAddr=orch:8935", "-httpAddr=0.0.0.0:8935", "-cliAddr=127.0.0.1:7935", "-orchSecret=probe", "-v=4"], "cpus": 2, "mem_limit": memory},
        "gateway": {"image": image, "command": ["-gateway", "-network=offchain", "-httpIngest", "-httpAddr=0.0.0.0:8935", "-cliAddr=127.0.0.1:7935", "-rtmpAddr=127.0.0.1:1936", "-orchAddr=orch:8935", "-v=4"], "cpus": 1, "mem_limit": "1g", "ports": ["127.0.0.1::8935"], "depends_on": ["orch"]}
    }}, indent=2))
    command = ["docker", "compose", "-p", project, "-f", str(compose)]
    receipt = {"image": image, "memory": memory, "sessions": [], "passed": False}

    def inspect():
        ids = run(*command, "ps", "--all", "-q").stdout.split()
        if not ids:
            return []
        rows = json.loads(run("docker", "inspect", *ids).stdout)
        return [{"name": row["Name"], "state": row["State"], "memory_limit": row["HostConfig"]["Memory"], "restarts": row["RestartCount"]} for row in rows]

    def ffmpeg(*args):
        return run("ffmpeg", *[arg.replace("/probe/", str(work)+"/") for arg in args])

    try:
        run(*command, "up", "-d")
        port = run(*command, "port", "gateway", "8935").stdout.strip()
        deadline = time.monotonic() + 40
        while True:
            try:
                urllib.request.urlopen(f"http://{port}/", timeout=2).close()
                break
            except urllib.error.HTTPError:
                break
            except OSError:
                if time.monotonic() > deadline:
                    raise
                time.sleep(0.25)
        # Keep preceding sessions alive, as scenario 66 does; a cold single
        # request can hide the aggregate memory held by idle transcoder sessions.
        for name, width, height, heights in [("qhd", 2560, 1440, [1440,1080,720,480,360]), ("uhd",3840,2160,[2160,1440,1080,720,480,360]), ("inherit",3840,2160,[1080,720,480,360])]:
            ffmpeg("-v","error","-y","-f","lavfi","-i",f"testsrc2=size={width}x{height}:rate=10","-t","8","-c:v","libx264","-threads","2","-preset","ultrafast","-g","20","-bf","0","-f","segment","-segment_time","2","-segment_format","mpegts",f"/probe/{name}-%d.ts")
            profiles = [{"name":f"{h}p","width":h*16//9//2*2,"height":h,"bitrate":{2160:24000000,1440:10800000,1080:6000000,720:3000000,480:1500000,360:800000}[h],"fps":10,"profile":"H264ConstrainedHigh"} for h in heights]
            configuration = json.dumps({"profiles":profiles,"workload":"vod","deadlineMs":120000,"minSpeed":0.001})
            session = {"name":name,"segments":[]}
            receipt["sessions"].append(session)
            segments = sorted(work.glob(f"{name}-[0-9].ts"))
            assert len(segments) == 4, f"expected four source segments for {name}, found {len(segments)}"
            for seq, source in enumerate(segments):
                request = urllib.request.Request(f"http://{port}/live/{project}-{name}/{seq}.ts", data=source.read_bytes(), headers={"Accept":"multipart/mixed","Content-Type":"video/mp2t","Content-Duration":"2000","Content-Resolution":f"{width}x{height}","Livepeer-Transcode-Configuration":configuration})
                started = time.monotonic()
                discovery_deadline = started + 35
                while True:
                    try:
                        with urllib.request.urlopen(request, timeout=135) as response:
                            body = response.read()
                            content_type = response.headers["Content-Type"]
                        break
                    except urllib.error.HTTPError as error:
                        states = inspect()
                        assert len(states) == 2 and all(not row["state"]["OOMKilled"] and row["state"]["Running"] for row in states), states
                        if name != "qhd" or seq != 0 or error.code != 503 or time.monotonic() >= discovery_deadline:
                            raise RuntimeError(f"segment {name}/{seq}: HTTP {error.code}: {error.read().decode(errors='replace')}") from error
                        time.sleep(1)
                message = email.message_from_bytes(b"Content-Type: "+content_type.encode()+b"\r\nMIME-Version: 1.0\r\n\r\n"+body, policy=email.policy.default)
                parts = list(message.iter_parts())
                assert len(parts) == len(heights), f"missing renditions: {len(parts)} versus {heights}"
                observed_heights = []
                for index, part in enumerate(parts):
                    output = work/f"{name}-{seq}-rendition-{index}.ts"
                    output.write_bytes(part.get_payload(decode=True))
                    probe = run("ffprobe","-v","error","-select_streams","v","-show_entries","stream=codec_name,width,height","-of","json",str(output))
                    track = json.loads(probe.stdout)["streams"][0]
                    assert track["codec_name"] == "h264" and min(track["width"],track["height"]) in heights, track
                    observed_heights.append(min(track["width"], track["height"]))
                assert sorted(observed_heights) == sorted(heights), observed_heights
                session["segments"].append({"seq":seq,"renditions":len(parts),"elapsed_seconds":time.monotonic()-started})
                states = inspect()
                assert len(states) == 2 and all(not row["state"]["OOMKilled"] and row["state"]["Running"] and row["restarts"] == 0 for row in states), states
        receipt["passed"] = True
    finally:
        receipt["states"] = inspect()
        (work/"receipt.json").write_text(json.dumps(receipt,indent=2)+"\n")
        (work/"compose.log").write_text(run(*command,"logs","--no-color",check=False).stdout)
        run(*command,"down",check=False)
        print(f"Livepeer {memory} memory probe: {work / 'receipt.json'}",flush=True)


if __name__ == "__main__":
    main()
