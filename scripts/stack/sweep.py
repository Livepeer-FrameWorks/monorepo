#!/usr/bin/env python3
"""Post-run invariant sweep for the stack suite (scripts/stack/sweep.sh).

Scenarios assert what they set out to test; this sweep asserts generic
properties of every entity the run created, whichever scenario created it:

  analytics  every stream that went live and ended has its stream_end event and
             a stream_sessions_final row; no analytics table holds a duplicate
             row of the run's streams; an ended stream's stream_runtime_5m
             runtime equals its final sessions' duration
  api        every VOD, DVR chapter VOD, clip and stream resolves through its
             type query and node(id) by the id the API returned for it
  artifacts  every artifact reaches its ready state; a thumbnail sprite covers
             the whole asset; renditions exist where processing requested them;
             no edge keeps a file of an artifact the run deleted (media,
             sidecars, relay caches under vod/, clips/, upload/, DVR trees)
  dvr        each recording's chapters cover the recorded range contiguously
             and none failed
  doctor     the operator CLI's cluster doctor reports no unexplained FAIL
  host       no container restarted or was OOM-killed, no core dump, no
             wildcard listener outside an edge's public set

Subcommands:
  run ...          collect from the API, ClickHouse, Postgres and Docker, check
  vtt <s> <file>   sprite coverage of a WebVTT file for an asset of <s> seconds
  chapters <file>  chapter coverage of {"recording":{...},"chapters":[...]}
  analytics <file> stream completeness of {"events":[...],"finals":[...],"sessions":[...]}
  runtime <file>   runtime ledger of {"finals":[...],"runtime":{...},"processed_ms":N,"live":[...]}
  doctor <file>    FAIL lines of a doctor log not explained by the allowlist
  renditions <file> owed renditions of {"processes":[...],"tracks":[...]}
  listeners <file> wildcard listeners of {"proc":{...},"public":{...}}
  leftovers <file> edge files of deleted artifacts in {"deleted":[...],"edges":{...}}

Every check prints PASS/FAIL/ALLOW lines naming the entity and the scenario
that created it. A scenario that produces a failure state on purpose declares
it with sweep_allow (scripts/stack/lib.sh): one invariant, one exact entity, a
regex over the failure detail, and the reason. An allowlist entry that matched
nothing is reported, so a stale one does not linger.
"""
import argparse
import base64
import json
import os
import re
import statistics
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

# ---- pure evaluators (unit-tested by scripts/stack/test_sweep.sh) ------------

CUE_RE = re.compile(r"((?:\d+:)?\d{1,2}:\d{2}\.\d{1,3})\s*-->\s*((?:\d+:)?\d{1,2}:\d{2}\.\d{1,3})")


def vtt_seconds(stamp):
    parts = stamp.split(":")
    total = 0.0
    for part in parts:
        total = total * 60 + float(part)
    return total


def vtt_cues(text):
    return [(vtt_seconds(a), vtt_seconds(b)) for a, b in CUE_RE.findall(text)]


def vtt_violations(text, duration):
    """Sprite coverage problems of a thumbnail VTT for an asset of `duration`
    seconds: the first cue must start within one interval of 0, the last must
    end within one interval of the duration, and no two consecutive cues may be
    more than two intervals apart. The interval is the median cue spacing."""
    cues = sorted(vtt_cues(text))
    if not cues:
        return ["sprite VTT has no cues"]
    starts = [s for s, _ in cues]
    spacing = [b - a for a, b in zip(starts, starts[1:]) if b > a]
    interval = statistics.median(spacing) if spacing else max(cues[0][1] - cues[0][0], 0.001)
    tolerance = interval + 0.5
    out = []
    first, last_end = starts[0], max(e for _, e in cues)
    summary = f"{len(cues)} cues every {interval:.1f}s, {first:.1f}s..{last_end:.1f}s of {duration:.1f}s"
    if first > tolerance:
        out.append(f"first cue starts at {first:.1f}s, not ~0 ({summary})")
    if duration > 0 and last_end < duration - tolerance:
        out.append(f"last cue ends at {last_end:.1f}s, short of the {duration:.1f}s duration ({summary})")
    for (a, _), (b, _) in zip(cues, cues[1:]):
        if b - a > 2 * interval + 0.5:
            out.append(f"{b - a:.1f}s hole between cues at {a:.1f}s and {b:.1f}s ({summary})")
            break
    return out


TERMINAL_CHAPTER_OK = {"FINALIZED", "FROZEN", "RECLAIMED"}


def chapter_violations(recording, chapters, end_slack_ms=60000, start_slack_ms=5000):
    """Coverage problems of one ended recording's chapters. recording carries
    startMs/endMs of the recorded range (0 when unknown); chapters carry
    startMs/endMs/state/hasGaps/segmentCount/lastFailureReason/playbackId."""
    out = []
    ordered = sorted(chapters, key=lambda c: c.get("startMs") or 0)
    if not ordered:
        return ["recording has no chapters"]
    for c in ordered:
        state = c.get("state") or "?"
        where = f"chapter {str(c.get('chapterId', '?'))[:12]} [{fmt_ms(c.get('startMs'))}..{fmt_ms(c.get('endMs'))})"
        if state.startswith("FAILED"):
            reason = c.get("lastFailureReason") or ""
            out.append(f"{where} {state}: {reason} (segments={c.get('segmentCount')})")
        elif state not in TERMINAL_CHAPTER_OK:
            out.append(f"{where} still {state}")
        elif not c.get("playbackId"):
            out.append(f"{where} {state} without a playback id")
        if c.get("hasGaps"):
            out.append(f"{where} has gaps")
    for a, b in zip(ordered, ordered[1:]):
        if (b.get("startMs") or 0) != (a.get("endMs") or 0):
            delta = (b.get("startMs") or 0) - (a.get("endMs") or 0)
            kind = "gap" if delta > 0 else "overlap"
            out.append(f"{kind} of {abs(delta) / 1000:.1f}s between chapters ending {fmt_ms(a.get('endMs'))} and starting {fmt_ms(b.get('startMs'))}")
    start, end = recording.get("startMs") or 0, recording.get("endMs") or 0
    if start and abs((ordered[0].get("startMs") or 0) - start) > start_slack_ms:
        out.append(f"first chapter starts at {fmt_ms(ordered[0].get('startMs'))}, the recording at {fmt_ms(start)}")
    if end and (ordered[-1].get("endMs") or 0) < end - end_slack_ms:
        out.append(f"last chapter ends at {fmt_ms(ordered[-1].get('endMs'))}, the recording at {fmt_ms(end)}")
    return out


def fmt_ms(ms):
    if not ms:
        return "?"
    return time.strftime("%H:%M:%S", time.gmtime(ms / 1000))


def bare_name(name):
    return name[5:] if name.startswith("live+") else name


def analytics_violations(events, finals, sessions):
    """Per stream that went live and ended: a stream_end at or after its last
    stream_start, and a stream_sessions_final row ending at or after it.

    events:   [{"stream":uuid,"name":internal,"type":"stream_start"|"stream_end","ts":epoch}]
    finals:   [{"stream":uuid,"started_ms":int,"ended_ms":int}]
    sessions: Foghorn ingest generations [{"name":internal,"started":epoch,"ended":epoch|0}],
              or None when Foghorn's database is not readable. A stream whose
              generation is still open has not ended; one Foghorn admitted with
              no stream_start in ClickHouse is missing its start.
    Returns {stream key: [violation, ...]}."""
    by_stream = {}
    names = {}
    for e in events:
        s = by_stream.setdefault(e["stream"], {"start": [], "end": []})
        s["start" if e["type"] == "stream_start" else "end"].append(e["ts"])
        names[bare_name(e.get("name") or "")] = e["stream"]
    final_ends = {}
    for f in finals:
        final_ends.setdefault(f["stream"], []).append(f["ended_ms"])
    open_names, admitted = set(), {}
    for g in sessions or []:
        n = bare_name(g["name"])
        admitted.setdefault(n, []).append(g)
        if not g.get("ended"):
            open_names.add(n)
    out = {}
    for name, gens in admitted.items():
        if name not in names and name not in open_names:
            out[name] = [f"Foghorn admitted {len(gens)} generation(s) since the run started, ClickHouse has no stream_start"]
    stream_names = {v: k for k, v in names.items()}
    for stream, s in by_stream.items():
        if stream_names.get(stream) in open_names:
            continue
        if not s["start"]:
            continue
        last_start = max(s["start"])
        problems = []
        if not any(t >= last_start for t in s["end"]):
            problems.append(f"no stream_end after its last stream_start at {fmt_ms(last_start * 1000)} "
                            f"({len(s['start'])} start(s), {len(s['end'])} end(s))")
        if not any(t >= last_start * 1000 - 1000 for t in final_ends.get(stream, [])):
            problems.append(f"no stream_sessions_final row ending after {fmt_ms(last_start * 1000)} "
                            f"({len(final_ends.get(stream, []))} row(s))")
        if problems:
            out[stream] = problems
    return out


def runtime_violations(finals, runtime, processed_ms, live):
    """Per ended stream whose final sessions the stream_runtime_5m rebuild has
    processed: the ledger's runtime equals the sessions' summed duration. Each
    5-minute window floors its seconds, so the ledger may fall short by one
    second per window; it may never exceed the sessions.

    finals:       [{"stream":uuid,"started_ms":int,"ended_ms":int,"projected_ms":int}]
    runtime:      {stream: {"seconds":int,"windows":int,"keys":int}} from stream_runtime_5m_v
    processed_ms: the stream_runtime_5m rebuild cursor
    live:         streams stream_state_current holds live, whose open session is
                  in the ledger without a final row
    Returns ({stream: [violation, ...]}, number of streams judged)."""
    by_stream = {}
    for f in finals:
        by_stream.setdefault(f["stream"], []).append(f)
    out, judged = {}, 0
    for stream, sessions in sorted(by_stream.items()):
        if stream in live or any(f["projected_ms"] >= processed_ms for f in sessions):
            continue
        judged += 1
        want = sum(max(0, f["ended_ms"] - f["started_ms"]) for f in sessions) // 1000
        got = runtime.get(stream, {"seconds": 0, "windows": 0, "keys": 0})
        if got["seconds"] > want + 1 or got["seconds"] < want - got["windows"] - 1:
            out[stream] = [f"stream_runtime_5m_v holds {got['seconds']}s under {got['keys']} session key(s), "
                           f"its {len(sessions)} final session(s) ran {want}s"]
    return out, judged


DOCTOR_FAIL_RE = re.compile(r"^\s*(?:\[FAIL\]|✗)\s*(.*?)\s*$")


def doctor_failures(text, allow):
    """FAIL lines of a cluster doctor log, each with the allowlist reason that
    explains it (None when nothing does). allow: [(regex, reason)]. The summary
    line ("... [FAIL] n/m healthy") is the count of the others, not a check."""
    out = []
    for line in text.splitlines():
        m = DOCTOR_FAIL_RE.match(line)
        if not m or re.match(r"^\d+/\d+ healthy$", m.group(1)):
            continue
        if re.search(r"\[FAIL\]\s*\d+/\d+ healthy", line):
            continue
        detail = m.group(1)
        reason = next((r for rx, r in allow if re.search(rx, detail)), None)
        out.append((detail, reason))
    return out


def parse_proc_net(text, proto):
    """Listening sockets of a /proc/net/{tcp,tcp6,udp,udp6} table as
    (proto, address, port). TCP listens in state 0A; a bound UDP socket with
    no remote end is a listener."""
    out = []
    for line in text.splitlines()[1:]:
        f = line.split()
        if len(f) < 4:
            continue
        local, remote, state = f[1], f[2], f[3]
        if proto == "tcp" and state != "0A":
            continue
        if proto == "udp" and not remote.endswith(":0000"):
            continue
        addr, port = local.split(":")
        out.append((proto, addr, int(port, 16)))
    return out


WILDCARD_ADDRS = {"00000000", "00000000000000000000000000000000", "0000000000000000FFFF000000000000"}


def wildcard_listeners(proc, public):
    """Wildcard listeners outside the public set. proc maps tcp/tcp6/udp/udp6
    to the /proc/net table text; public maps tcp/udp to allowed ports, and
    udp_range to an allowed [low, high] (the ephemeral range WebRTC's outgoing
    sockets bind in)."""
    seen = set()
    for table, text in proc.items():
        proto = "tcp" if table.startswith("tcp") else "udp"
        for p, addr, port in parse_proc_net(text, proto):
            if addr.upper() not in WILDCARD_ADDRS:
                continue
            if port in set(public.get(p, [])):
                continue
            low, high = (public.get(f"{p}_range") or [0, -1])
            if low <= port <= high:
                continue
            seen.add((p, port))
    return sorted(seen)


def deleted_leftovers(deleted, edges):
    """Edge paths still holding a deleted artifact, per "<kind> <hash>" entity.
    deleted is [{"kind","hash"}]; edges maps an edge to the paths under its
    storage root. A path belongs to an artifact when its name is the hash (a
    DVR tree) or starts with "<hash>." (media, .partial, .dtsh, .gop, .blocks)."""
    out = {}
    for d in deleted:
        h = d["hash"]
        problems = []
        for edge, paths in sorted(edges.items()):
            hits = sorted(p for p in paths if os.path.basename(p.rstrip("/")) == h
                          or os.path.basename(p.rstrip("/")).startswith(h + "."))
            if hits:
                problems.append(f"{edge} keeps {len(hits)} path(s): {' '.join(hits[:6])}")
        if problems:
            out[f"{d['kind']} {h}"] = problems
    return out


# ---- allowlist, scenario attribution, result lines ---------------------------


class Report:
    def __init__(self, allow, windows):
        self.allow = allow
        self.windows = windows
        self.used = set()
        self.fail = 0
        self.blocked = 0
        self.passed = 0

    def scenario_of(self, epoch):
        if not epoch:
            return "unknown scenario"
        best = None
        for name, start, end in self.windows:
            if start <= epoch <= end:
                return name
            if start <= epoch:
                best = f"after {name}"
        return best or "bring-up"

    def allowed(self, invariant, entity, detail):
        for i, (scenario, inv, ent, rx, reason) in enumerate(self.allow):
            if inv == invariant and ent == entity and re.search(rx, detail):
                self.used.add(i)
                return f"{scenario}: {reason}"
        return None

    def check(self, invariant, entity, problems, at=None, scenario=None):
        scenario = scenario or self.scenario_of(at)
        bad = []
        for p in problems:
            why = self.allowed(invariant, entity, p)
            if why:
                print(f"ALLOW sweep[{invariant}]: {entity} ({scenario}): {p} -- allowlisted by {why}")
            else:
                bad.append(p)
        for p in bad:
            self.fail += 1
            print(f"FAIL  sweep[{invariant}]: {entity} ({scenario}): {p}")
        if not bad:
            self.passed += 1
        return not bad

    def block(self, invariant, why):
        self.blocked += 1
        print(f"BLOCKED  sweep[{invariant}]: {why}")

    def note(self, invariant, text):
        print(f"PASS  sweep[{invariant}]: {text}")

    def unused_allow(self):
        for i, (scenario, inv, ent, rx, reason) in enumerate(self.allow):
            if i not in self.used:
                print(f"NOTE  sweep: allowlist entry of {scenario} ({inv} {ent} /{rx}/) matched nothing")


def load_allow(paths):
    out = []
    for path in paths:
        if not path or not os.path.exists(path):
            continue
        with open(path) as f:
            for line in f:
                parts = line.rstrip("\n").split("\t")
                if len(parts) == 5 and not line.startswith("#"):
                    out.append(tuple(parts))
    return out


def load_windows(path):
    out = []
    if path and os.path.exists(path):
        with open(path) as f:
            for line in f:
                parts = line.rstrip("\n").split("\t")
                if len(parts) == 3:
                    out.append((parts[0], int(parts[1]), int(parts[2]) if parts[2] else 2**62))
    return out


# ---- collectors ---------------------------------------------------------------


class Sources:
    def __init__(self, env):
        self.env = env
        self.bridge = env.get("BRIDGE_URL", "http://bridge:18000")
        self.token = env.get("STACK_API_TOKEN", "")
        self.tenant = env.get("STACK_TENANT_ID", "")
        self.ch_url = env.get("CLICKHOUSE_URL", "")
        self.chandler = env.get("CHANDLER_URL", "")

    def gql(self, query, variables=None):
        body = json.dumps({"query": query, "variables": variables or {}}).encode()
        req = urllib.request.Request(self.bridge + "/graphql", data=body, headers={
            "Authorization": "Bearer " + self.token, "Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                return json.loads(r.read().decode())
        except urllib.error.HTTPError as e:
            try:
                return json.loads(e.read().decode())
            except Exception:
                return {"errors": [{"message": f"HTTP {e.code}"}]}
        except Exception as e:
            return {"errors": [{"message": f"{type(e).__name__}: {e}"}]}

    def ch(self, sql):
        """Rows of a TSV query, or None when ClickHouse is not reachable."""
        if not self.ch_url:
            return None
        try:
            req = urllib.request.Request(self.ch_url, data=(sql + " FORMAT TSV").encode())
            with urllib.request.urlopen(req, timeout=60) as r:
                text = r.read().decode()
        except Exception as e:
            print(f"    clickhouse: {type(e).__name__}: {str(e)[:160]}", file=sys.stderr)
            return None
        return [line.split("\t") for line in text.splitlines() if line]

    def pg(self, db, sql):
        """Rows of a psql query as the service's own role, or None."""
        cmd = ["psql", "-h", self.env.get("PG_HOST", "postgres"), "-p", self.env.get("PG_PORT", "5432"),
               "-U", db, "-d", db, "-v", "ON_ERROR_STOP=1", "-Atc", sql]
        try:
            r = subprocess.run(cmd, capture_output=True, text=True, timeout=60, stdin=subprocess.DEVNULL,
                               env={**os.environ, "PGPASSWORD": self.env.get("POSTGRES_PASSWORD", "")})
        except (OSError, subprocess.TimeoutExpired):
            return None
        if r.returncode != 0:
            return None
        return [line.split("|") for line in r.stdout.splitlines() if line]

    def fetch(self, url):
        candidates = [url]
        m = re.search(r"(/assets/.*)$", url)
        if m and self.chandler:
            candidates.append(self.chandler.rstrip("/") + m.group(1))
        last = ""
        for u in candidates:
            try:
                with urllib.request.urlopen(u, timeout=20) as r:
                    return r.read().decode("utf-8", "replace"), None
            except Exception as e:
                last = f"{u}: {type(e).__name__}: {str(e)[:120]}"
        return None, last


def gql_errors(resp):
    errs = resp.get("errors") or []
    return "; ".join(e.get("message", "?") for e in errs)[:300]


def relay_id(typ, raw):
    return base64.b64encode(f"{typ}:{raw}".encode()).decode()


ARTIFACT_FIELDS = """key kind id hash playbackId streamId status errorMessage createdAt durationSeconds
  thumbnailAssets{spriteVttUrl assetKey} tracks{type codec width height}"""


def list_artifacts(src, since):
    """Every storage artifact created since the run started, newest first."""
    out, offset = [], 0
    while True:
        r = src.gql(f"query($i:StorageArtifactsInput){{storageArtifactsConnection(input:$i){{nodes{{{ARTIFACT_FIELDS}}} hasNextPage}}}}",
                    {"i": {"first": 100, "offset": offset, "sort": "CREATED_AT", "direction": "DESC"}})
        conn = (r.get("data") or {}).get("storageArtifactsConnection")
        if not conn:
            return None, gql_errors(r)
        older = False
        for n in conn["nodes"]:
            if iso_epoch(n.get("createdAt")) < since:
                older = True
                continue
            out.append(n)
        if older or not conn.get("hasNextPage"):
            return out, None
        offset += 100


def iso_epoch(stamp):
    if not stamp:
        return 0
    s = re.sub(r"\.\d+", "", stamp).replace("Z", "+00:00")
    try:
        from datetime import datetime
        return int(datetime.fromisoformat(s).timestamp())
    except ValueError:
        return 0


READY = {"VOD": {"ready"}, "CHAPTER": {"ready"}, "CLIP": {"ready"}, "DVR": {"completed", "ready"}}
PENDING = {"uploading", "processing", "queued", "requested", "pending", "recording", "finalizing", "freezing"}


def artifact_state_problems(a):
    status = (a.get("status") or "").lower()
    if status in READY.get(a["kind"], {"ready"}):
        return []
    if status in PENDING:
        return [f"still {status}"]
    detail = f" ({a['errorMessage']})" if a.get("errorMessage") else ""
    return [f"status {status or '?'}{detail}"]


def roundtrip_problems(src, a):
    """The artifact resolves through its type query by hash and by the relay id
    the API returned for it, and through node(id), each answering the same hash."""
    out = []
    if a["kind"] in ("VOD", "CHAPTER"):
        typ, query, field = "VodAsset", "vodAsset", "artifactHash"
    elif a["kind"] == "CLIP":
        typ, query, field = "Clip", "clip", "clipHash"
    else:
        return out
    by_hash = src.gql(f"query($id:ID!){{{query}(id:$id){{id {field}}}}}", {"id": a["hash"]})
    obj = (by_hash.get("data") or {}).get(query)
    if not obj or obj.get(field) != a["hash"]:
        out.append(f"{query}(id: hash) -> {gql_errors(by_hash) or json.dumps(obj)}")
        rid = relay_id(typ, a["hash"])
    else:
        rid = obj["id"]
    by_id = src.gql(f"query($id:ID!){{{query}(id:$id){{id {field}}}}}", {"id": rid})
    obj = (by_id.get("data") or {}).get(query)
    if not obj or obj.get(field) != a["hash"] or obj.get("id") != rid:
        out.append(f"{query}(id: {rid}) -> {gql_errors(by_id) or json.dumps(obj)}")
    node = src.gql(f"query($id:ID!){{node(id:$id){{id ... on {typ}{{{field}}}}}}}", {"id": rid})
    obj = (node.get("data") or {}).get("node")
    if not obj or obj.get(field) != a["hash"] or obj.get("id") != rid:
        out.append(f"node(id: {rid}) -> {gql_errors(node) or json.dumps(obj)}")
    return out


def thumbnail_problems(src, a, min_duration):
    if a["kind"] == "DVR":
        return []
    duration = a.get("durationSeconds") or 0
    assets = a.get("thumbnailAssets")
    if not duration and a["kind"] in ("VOD", "CHAPTER"):
        r = src.gql("query($id:ID!){vodAsset(id:$id){durationMs thumbnailAssets{spriteVttUrl assetKey}}}", {"id": a["hash"]})
        v = (r.get("data") or {}).get("vodAsset") or {}
        duration = (v.get("durationMs") or 0) / 1000
        assets = assets or v.get("thumbnailAssets")
    if not assets:
        return ["ready without thumbnails"] if duration >= min_duration else []
    text, err = src.fetch(assets["spriteVttUrl"])
    if text is None:
        return [f"sprite VTT unreadable: {err}"]
    return vtt_violations(text, duration)


def requested_heights(processes):
    """Rendition heights a processing job's processes ask for: Livepeer target
    profiles and AV resolutions."""
    out = set()
    for p in processes:
        if p.get("process") == "Livepeer":
            out |= {int(t.get("height") or 0) for t in p.get("target_profiles") or []}
        elif p.get("process") == "AV" and re.match(r"^\d+x\d+$", str(p.get("resolution") or "")):
            out.add(int(str(p["resolution"]).split("x")[1]))
    return {h for h in out if h > 0}


def rendition_violations(processes, tracks):
    """Renditions never upscale, so a job owes the renditions below its
    source's height; any owed one must have left a second video track."""
    video = [t for t in tracks if t.get("type") == "video" and (t.get("codec") or "").upper() not in ("JPEG", "PNG")]
    if not video:
        return ["finalized without a video track"]
    source = max(int(t.get("height") or 0) for t in video)
    owed = sorted(h for h in requested_heights(processes) if h < source)
    if owed and len(video) < 2:
        return [f"processing owed renditions at {', '.join(f'{h}p' for h in owed)} below the {source}p source "
                f"but the artifact has {len(video)} video track(s)"]
    return []


def rendition_problems(src, a, foghorn_dbs):
    """A processing job that owed renditions left them on the finalized artifact."""
    if a["kind"] != "VOD":
        return []
    procs = None
    for db in foghorn_dbs:
        rows = src.pg(db, f"SELECT COALESCE(processes_json,'') FROM foghorn.processing_jobs WHERE artifact_hash='{a['hash']}' "
                          f"AND job_type <> 'thumbnail' ORDER BY created_at DESC LIMIT 1")
        if rows:
            procs = rows[0][0]
            break
    if not procs:
        return []
    try:
        processes = [p for p in json.loads(procs) if isinstance(p, dict)]
    except ValueError:
        return []
    return rendition_violations(processes, a.get("tracks") or [])


def dvr_chapters(src, dvr_hash):
    chapters, token = [], None
    while True:
        v = {"d": dvr_hash, "n": 1000}
        if token:
            v["t"] = token
        r = src.gql("query($d:ID!,$n:Int,$t:String){dvrChapters(dvrId:$d,pageSize:$n,pageToken:$t){chapters{chapterId state startMs endMs isCurrent playbackId segmentCount hasGaps lastFailureReason} nextPageToken}}", v)
        page = (r.get("data") or {}).get("dvrChapters")
        if page is None:
            return None, gql_errors(r)
        chapters += page["chapters"]
        token = page.get("nextPageToken")
        if not token:
            return chapters, None


def recording_range(src, foghorn_dbs, dvr_hash):
    for db in foghorn_dbs:
        rows = src.pg(db, f"SELECT COALESCE(floor(EXTRACT(EPOCH FROM started_at)*1000)::bigint,0), "
                          f"COALESCE(floor(EXTRACT(EPOCH FROM ended_at)*1000)::bigint,0) "
                          f"FROM foghorn.artifacts WHERE artifact_hash='{dvr_hash}'")
        if rows:
            return {"startMs": int(rows[0][0] or 0), "endMs": int(rows[0][1] or 0)}
    return {}


ANALYTICS_TABLES = ["stream_health_samples", "viewer_connection_events", "routing_decisions", "client_qoe_samples",
                    "track_list_events", "stream_event_log", "viewer_sessions_final", "stream_sessions_final",
                    "processing_events"]


def collect_analytics(src, since, foghorn_dbs, scope):
    t = src.tenant
    rows = src.ch(f"SELECT toString(stream_id), internal_name, event_type, toUnixTimestamp(timestamp) FROM periscope.stream_event_log "
                  f"WHERE tenant_id = toUUID('{t}') AND timestamp >= toDateTime({since}) AND event_type IN ('stream_start','stream_end')")
    if rows is None:
        return None
    events = [{"stream": r[0], "name": r[1], "type": r[2], "ts": int(r[3])} for r in rows
              if not scope or r[0] in scope]
    rows = src.ch(f"SELECT toString(stream_id), source_started_at_ms, source_ended_at_ms FROM periscope.stream_sessions_final "
                  f"WHERE tenant_id = toUUID('{t}') AND source_ended_at_ms >= {since * 1000}") or []
    finals = [{"stream": r[0], "started_ms": int(r[1]), "ended_ms": int(r[2])} for r in rows]
    sessions = None
    for db in foghorn_dbs:
        rows = src.pg(db, f"SELECT stream_internal_name, floor(EXTRACT(EPOCH FROM started_at))::bigint, "
                          f"COALESCE(floor(EXTRACT(EPOCH FROM ended_at))::bigint, 0) FROM foghorn.ingest_sessions "
                          f"WHERE tenant_id='{t}' AND started_at >= to_timestamp({since}) AND projection_state <> 'pending'")
        if rows is None:
            continue
        sessions = (sessions or []) + [{"name": r[0], "started": int(r[1]), "ended": int(r[2])} for r in rows]
    if sessions is not None and scope:
        names = {bare_name(e["name"]) for e in events}
        sessions = [g for g in sessions if bare_name(g["name"]) in names]
    return {"events": events, "finals": finals, "sessions": sessions}


def collect_runtime(src, since, streams):
    """Inputs of runtime_violations for the run's streams, or None when
    ClickHouse is not reachable."""
    if not streams:
        return {"finals": [], "runtime": {}, "processed_ms": 0, "live": set()}
    t = src.tenant
    ids = ",".join(f"toUUID('{s}')" for s in sorted(streams))
    rows = src.ch("SELECT max(last_processed_projection_ms) FROM periscope.ledger_rebuild_cursors_v2 "
                  "WHERE ledger_name = 'stream_runtime_5m'")
    if rows is None:
        return None
    processed_ms = int(rows[0][0] or 0) if rows else 0
    rows = src.ch(f"SELECT toString(stream_id), source_started_at_ms, source_ended_at_ms, latest_projection_version_ms "
                  f"FROM periscope.stream_sessions_final_v WHERE tenant_id = toUUID('{t}') AND stream_id IN ({ids}) "
                  f"AND closed_reason = 'final' AND source_ended_at_ms >= {since * 1000}") or []
    finals = [{"stream": r[0], "started_ms": int(r[1]), "ended_ms": int(r[2]), "projected_ms": int(r[3])} for r in rows]
    rows = src.ch(f"SELECT toString(stream_id), sum(active_seconds), uniqExact(window_start), uniqExact(source_event_id) "
                  f"FROM periscope.stream_runtime_5m_v WHERE tenant_id = toUUID('{t}') AND stream_id IN ({ids}) "
                  f"GROUP BY stream_id") or []
    runtime = {r[0]: {"seconds": int(r[1]), "windows": int(r[2]), "keys": int(r[3])} for r in rows}
    rows = src.ch(f"SELECT toString(stream_id) FROM periscope.stream_state_current FINAL "
                  f"WHERE tenant_id = toUUID('{t}') AND stream_id IN ({ids}) AND status = 'live'") or []
    return {"finals": finals, "runtime": runtime, "processed_ms": processed_ms, "live": {r[0] for r in rows}}


def duplicate_rows(src, streams):
    """count() - uniqExact(record key) per analytics table over the run's
    streams: a redelivered Kafka record written twice. The key is the table's
    event_id, else session_id, else ingest_id (the Kafka record identity)."""
    if not streams:
        return {}
    ids = ",".join(f"toUUID('{s}')" for s in sorted(streams))
    out = {}
    for table in ANALYTICS_TABLES:
        key = src.ch(f"SELECT name FROM system.columns WHERE database='periscope' AND table='{table}' "
                     f"AND name IN ('event_id','session_id','ingest_id') ORDER BY name='event_id' DESC, name='session_id' DESC LIMIT 1")
        if not key:
            continue
        rows = src.ch(f"SELECT count(), uniqExact({key[0][0]}) FROM periscope.{table} WHERE stream_id IN ({ids})")
        if rows:
            out[f"{table} [{key[0][0]}]"] = (int(rows[0][0]), int(rows[0][1]))
    return out


def docker(*args):
    try:
        r = subprocess.run(["docker", *args], capture_output=True, text=True, timeout=60)
    except (OSError, subprocess.TimeoutExpired):
        return None
    return r.stdout if r.returncode == 0 else None


def run(args):
    env = os.environ
    src = Sources(env)
    report = Report(load_allow(args.allow), load_windows(args.windows))
    since = args.since
    staging = env.get("STACK_TARGET", "stack") == "staging"
    foghorn_dbs = [d for d in (env.get("FOGHORN_A_DB", "foghorn"), env.get("FOGHORN_B_DB", "foghorn_b")) if d]
    if src.pg(foghorn_dbs[0], "SELECT 1") is None:
        foghorn_dbs = []
    deadline = time.time() + args.wait
    started = time.time()

    # ---- analytics ----
    scope = None
    if staging:
        scope = set(ledger_streams(args.ledger, since))
    data = None
    while True:
        data = collect_analytics(src, since, foghorn_dbs, scope)
        if data is None or not analytics_violations(**data) or time.time() >= deadline:
            break
        time.sleep(10)
    if data is None:
        report.block("analytics", "ClickHouse is not reachable (CLICKHOUSE_URL)")
    else:
        bad = analytics_violations(**data)
        first_seen = {}
        for e in data["events"]:
            first_seen[e["stream"]] = min(first_seen.get(e["stream"], e["ts"]), e["ts"])
            first_seen[bare_name(e["name"])] = first_seen[e["stream"]]
        for g in data["sessions"] or []:
            first_seen.setdefault(bare_name(g["name"]), g["started"])
        streams = {e["stream"] for e in data["events"]}
        for key, problems in sorted(bad.items()):
            report.check("analytics", key, problems, at=first_seen.get(key))
        report.note("analytics", f"{len(streams) - len(bad)}/{len(streams)} streams have their stream_end and final session row"
                    + ("" if data["sessions"] is not None else " (Foghorn sessions unreadable: ClickHouse-only check)"))
        dups = duplicate_rows(src, streams)
        for table, (count, uniq) in dups.items():
            report.check("analytics", table, [f"{count - uniq} duplicate row(s) of {count}"] if count != uniq else [],
                         scenario="run")
        if dups:
            report.note("analytics", f"duplicate check over {len(dups)} tables, {len(streams)} streams")
        # The runtime ledger rebuilds every five minutes behind a settlement
        # lag, so a stream is judged once the rebuild cursor passed its finals.
        while True:
            rt = collect_runtime(src, since, streams)
            if rt is None or time.time() >= deadline or all(
                    f["stream"] in rt["live"] or f["projected_ms"] < rt["processed_ms"] for f in rt["finals"]):
                break
            time.sleep(10)
        if rt is None:
            report.block("analytics", "ClickHouse is not reachable for the stream runtime ledger")
        else:
            bad, judged = runtime_violations(**rt)
            for stream, problems in sorted(bad.items()):
                report.check("analytics", f"runtime {stream}", problems, at=first_seen.get(stream))
            report.note("analytics", f"{judged - len(bad)}/{judged} ended streams the runtime ledger processed hold "
                        f"their final sessions' runtime ({len({f['stream'] for f in rt['finals']})} with final sessions)")

    # ---- artifacts, api, dvr ----
    artifacts, err = list_artifacts(src, since)
    while artifacts is not None and time.time() < deadline and any(
            (a.get("status") or "").lower() in PENDING for a in artifacts):
        time.sleep(10)
        artifacts, err = list_artifacts(src, since)
    if artifacts is None:
        report.block("artifacts", f"storageArtifactsConnection: {err}")
        artifacts = []
    kinds = {}
    recordings = 0
    for a in artifacts:
        kinds[a["kind"]] = kinds.get(a["kind"], 0) + 1
        at = iso_epoch(a.get("createdAt"))
        entity = f"{a['kind'].lower()} {a['hash']}"
        state = artifact_state_problems(a)
        report.check("artifacts", entity, state, at=at)
        report.check("api", entity, roundtrip_problems(src, a), at=at)
        if not state:
            report.check("artifacts", entity, thumbnail_problems(src, a, args.thumb_min) + rendition_problems(src, a, foghorn_dbs), at=at)
        if a["kind"] == "DVR" and (a.get("status") or "").lower() in READY["DVR"]:
            recordings += 1
            chapters, cerr = dvr_chapters(src, a["hash"])
            if chapters is None:
                report.check("dvr", entity, [f"dvrChapters: {cerr}"], at=at)
            else:
                report.check("dvr", entity, chapter_violations(recording_range(src, foghorn_dbs, a["hash"]), chapters), at=at)
    report.note("artifacts", "checked " + (", ".join(f"{n} {k}" for k, n in sorted(kinds.items())) or "no artifacts"))
    report.note("dvr", f"checked the chapters of {recordings} ended recording(s)")
    if staging:
        report.note("artifacts", "staging edges' storage is not inspected for deleted artifacts")
    else:
        deleted_artifact_checks(report, src, foghorn_dbs, since, max(deadline, time.time() + 120))

    # ---- streams: round-trip captured before each deletion, plus live ones ----
    entries = ledger_entries(args.ledger, since)
    for e in entries:
        # A stream created from a `bash -c ... _` helper carries "_" as its scenario.
        if not re.match(r"^\d\d-", e.get("scenario") or ""):
            e["scenario"] = report.scenario_of(e.get("t"))
    deleted = {e["id"]: e for e in entries if e.get("kind") == "deleted"}
    listed, lerr = list_stream_ids(src)
    if listed is None:
        report.block("api", f"streamsConnection: {lerr}")
        listed = set()
    # A stream a scenario deleted without delete_stream (a raw deleteStream) is
    # absent from the listing and has no pre-deletion answer to check.
    live = {e["id"]: e for e in entries if e.get("kind") == "created" and e["id"] not in deleted and e["id"] in listed}
    for sid, entry in live.items():
        entry = dict(entry)
        entry["stream"] = src.gql("query($id:ID!){stream(id:$id){id streamId}}", {"id": sid})
        entry["node"] = src.gql("query($id:ID!){node(id:$id){id ... on Stream{streamId}}}", {"id": sid})
        deleted[sid] = entry
    for sid, entry in deleted.items():
        problems = []
        when = "before deletion" if entry.get("kind") == "deleted" else "at the sweep"
        for query in ("stream", "node"):
            resp = entry.get(query) or {}
            obj = (resp.get("data") or {}).get(query)
            if not obj or obj.get("id") != sid:
                problems.append(f"{query}(id: {sid}) {when} -> {gql_errors(resp) or json.dumps(obj)}")
        report.check("api", f"stream {sid}", problems, scenario=entry.get("scenario"))
    report.note("api", f"{len(deleted)} streams round-tripped ({len(live)} still present at the sweep)")

    # ---- host ----
    if staging:
        report.note("host", "staging host health is the doctor's Host crashes and Listeners checks")
    else:
        host_checks(report, args)
    report.unused_allow()
    print(f"\nsweep: PASS={report.passed} FAIL={report.fail} BLOCKED={report.blocked} ({time.time() - started:.0f}s)")
    if report.fail:
        return 1
    return 2 if report.blocked else 0


EDGE_STORAGE = "/data/storage"


def deleted_artifact_checks(report, src, foghorn_dbs, since, deadline):
    """No edge keeps a file of an artifact deleted during the run. A delete an
    active read's lease defers completes when the read ends, so the check waits
    until deadline for the edges to settle."""
    when = {}
    for db in foghorn_dbs:
        rows = src.pg(db, f"""SELECT artifact_type, artifact_hash, extract(epoch FROM updated_at)::bigint
            FROM foghorn.artifacts WHERE status = 'deleted' AND updated_at >= to_timestamp({int(since)})
            AND artifact_type IN ('vod', 'clip', 'chapter', 'dvr')""")
        if rows is None:
            report.block("artifacts", f"{db}: deleted artifacts unreadable")
            return
        for kind, h, at in rows:
            when.setdefault(h, (kind, int(at)))
    deleted = [{"kind": kind, "hash": h} for h, (kind, _) in sorted(when.items())]
    project = os.environ.get("COMPOSE_PROJECT_NAME", "")
    while True:
        edges = {}
        for svc in ("edge", "edge-b"):
            cid = (docker("compose", "-p", project, "ps", "-q", svc) or "").strip()
            if not cid:
                continue
            listing = docker("exec", cid, "find", EDGE_STORAGE, "-maxdepth", "3")
            if listing is None:
                report.block("artifacts", f"container {svc}: storage listing failed")
                return
            edges[svc] = listing.split()
        left = deleted_leftovers(deleted, edges)
        if not left or time.time() >= deadline:
            break
        time.sleep(10)
    for d in deleted:
        entity = f"{d['kind']} {d['hash']}"
        report.check("artifacts", entity, left.get(entity, []), at=when[d["hash"]][1])
    report.note("artifacts", f"{len(deleted) - len(left)}/{len(deleted)} deleted artifacts left no file on {len(edges)} edge(s)")


def list_stream_ids(src):
    """Relay ids of every stream the API lists for the tenant."""
    ids, after = set(), None
    while True:
        page = {"first": 500}
        if after:
            page["after"] = after
        r = src.gql("query($p:ConnectionInput){streamsConnection(page:$p){nodes{id} pageInfo{hasNextPage endCursor}}}", {"p": page})
        conn = (r.get("data") or {}).get("streamsConnection")
        if not conn:
            return None, gql_errors(r)
        ids |= {n["id"] for n in conn["nodes"]}
        info = conn.get("pageInfo") or {}
        if not info.get("hasNextPage") or not info.get("endCursor"):
            return ids, None
        after = info["endCursor"]


def ledger_entries(path, since):
    if not path or not os.path.exists(path):
        return []
    out = []
    with open(path) as f:
        for line in f:
            try:
                e = json.loads(line)
            except ValueError:
                continue
            if e.get("t", 0) >= since:
                out.append(e)
    return out


def ledger_streams(path, since):
    return [e["stream_uuid"] for e in ledger_entries(path, since) if e.get("kind") == "created" and e.get("stream_uuid")]


EDGE_PUBLIC = {"tcp": [22, 80, 443, 1935, 5554, 4200], "udp": [443, 8889, 18203]}


def host_checks(report, args):
    project = os.environ.get("COMPOSE_PROJECT_NAME", "")
    ids = (docker("ps", "-a", "-q", "--filter", f"label=com.docker.compose.project={project}") or "").split()
    if not ids:
        report.block("host", "no docker access to the slot's containers")
        return
    baseline = {}
    if args.baseline and os.path.exists(args.baseline):
        with open(args.baseline) as f:
            for line in f:
                p = line.split()
                if len(p) == 2:
                    baseline[p[0]] = int(p[1])
    out = docker("inspect", "-f", "{{index .Config.Labels \"com.docker.compose.service\"}} {{.RestartCount}} {{.State.OOMKilled}} {{.State.Status}}", *ids) or ""
    for line in out.splitlines():
        svc, restarts, oom, status = line.split()
        problems = []
        if int(restarts) > baseline.get(svc, 0):
            problems.append(f"restarted {int(restarts) - baseline.get(svc, 0)} time(s) by its restart policy")
        if oom == "true":
            problems.append("OOM-killed")
        report.check("host", f"container {svc}", problems, scenario="run")
    for svc in ("edge", "edge-b"):
        cid = (docker("compose", "-p", project, "ps", "-q", svc) or "").strip()
        if not cid:
            continue
        cores = docker("exec", cid, "sh", "-c", "find / -xdev \\( -name core -o -name 'core.[0-9]*' \\) -type f 2>/dev/null | head -5")
        report.check("host", f"container {svc}", [f"core dump {c}" for c in (cores or "").split()], scenario="run")
        proc = {}
        for table in ("tcp", "tcp6", "udp", "udp6"):
            proc[table] = docker("exec", cid, "cat", f"/proc/net/{table}") or ""
        public = dict(EDGE_PUBLIC)
        public.update(edge_public_extra(cid))
        rng = (docker("exec", cid, "cat", "/proc/sys/net/ipv4/ip_local_port_range") or "").split()
        if len(rng) == 2:
            public["udp_range"] = [int(rng[0]), int(rng[1])]
        for allowed in os.environ.get("STACK_SWEEP_EDGE_INTERNAL_PORTS", "").split():
            p, _, port = allowed.partition("/")
            public.setdefault(p, []).append(int(port))
        bad = wildcard_listeners(proc, public)
        report.check("host", f"container {svc}", [f"{p} port {port} listens on every interface outside the edge's public set" for p, port in bad], scenario="run")


def edge_public_extra(cid):
    """Ports the edge's Mist config sets for its public connectors."""
    text = docker("exec", cid, "sh", "-c", "cat /etc/frameworks/mistserver.conf 2>/dev/null || cat /etc/mistserver.conf 2>/dev/null") or ""
    try:
        conf = json.loads(text)
    except ValueError:
        return {}
    extra = {"tcp": [], "udp": []}
    protos = {"RTMP": "tcp", "RTSP": "tcp", "DTSC": "tcp", "TSSRT": "udp", "WebRTC": "udp"}
    for proto in (conf.get("config") or {}).get("protocols") or []:
        name = proto.get("connector")
        if name in protos and proto.get("port"):
            extra[protos[name]].append(int(proto["port"]))
    return {k: EDGE_PUBLIC[k] + v for k, v in extra.items()}


# ---- CLI ----------------------------------------------------------------------


def main(argv):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("run")
    r.add_argument("--since", type=int, required=True)
    r.add_argument("--windows")
    r.add_argument("--allow", action="append", default=[])
    r.add_argument("--ledger")
    r.add_argument("--baseline")
    r.add_argument("--wait", type=int, default=240)
    r.add_argument("--thumb-min", type=float, default=10.0)
    v = sub.add_parser("vtt")
    v.add_argument("duration", type=float)
    v.add_argument("file")
    c = sub.add_parser("chapters")
    c.add_argument("file")
    a = sub.add_parser("analytics")
    a.add_argument("file")
    rt = sub.add_parser("runtime")
    rt.add_argument("file")
    d = sub.add_parser("doctor")
    d.add_argument("file")
    d.add_argument("allow", nargs="?")
    rn = sub.add_parser("renditions")
    rn.add_argument("file")
    li = sub.add_parser("listeners")
    li.add_argument("file")
    lo = sub.add_parser("leftovers")
    lo.add_argument("file")
    o = p.parse_args(argv)

    if o.cmd == "run":
        return run(o)
    if o.cmd == "vtt":
        problems = vtt_violations(open(o.file).read(), o.duration)
    elif o.cmd == "chapters":
        doc = json.load(open(o.file))
        problems = chapter_violations(doc.get("recording") or {}, doc["chapters"])
    elif o.cmd == "analytics":
        doc = json.load(open(o.file))
        problems = [f"{k}: {p}" for k, ps in sorted(analytics_violations(doc["events"], doc["finals"], doc.get("sessions")).items()) for p in ps]
    elif o.cmd == "runtime":
        doc = json.load(open(o.file))
        bad, _ = runtime_violations(doc["finals"], doc["runtime"], doc["processed_ms"], set(doc.get("live") or []))
        problems = [f"{k}: {p}" for k, ps in sorted(bad.items()) for p in ps]
    elif o.cmd == "doctor":
        allow = []
        if o.allow:
            for line in open(o.allow):
                if line.strip() and not line.startswith("#"):
                    rx, _, reason = line.rstrip("\n").partition("\t")
                    allow.append((rx, reason))
        rows = doctor_failures(open(o.file).read(), allow)
        for detail, reason in rows:
            print(f"ALLOW {detail} -- {reason}" if reason else f"FAIL  {detail}")
        return 1 if any(reason is None for _, reason in rows) else 0
    elif o.cmd == "renditions":
        doc = json.load(open(o.file))
        problems = rendition_violations(doc["processes"], doc["tracks"])
    elif o.cmd == "listeners":
        doc = json.load(open(o.file))
        problems = [f"{p} {port}" for p, port in wildcard_listeners(doc["proc"], doc["public"])]
    elif o.cmd == "leftovers":
        doc = json.load(open(o.file))
        problems = [f"{k}: {p}" for k, ps in sorted(deleted_leftovers(doc["deleted"], doc["edges"]).items()) for p in ps]
    for line in problems:
        print(line)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
