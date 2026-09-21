"""The serverInfo gate: what the client learned from the server's version."""

from __future__ import annotations

import re
import threading
from dataclasses import dataclass, field

from .errors import FrameWorksError, ServerTooOldError, UnsupportedOperationError

_STABLE_VERSION = re.compile(r"^v?(\d+)\.(\d+)\.(\d+)$")


@dataclass(frozen=True)
class ServerStatus:
    """version is the server's release version, or None when the server
    predates serverInfo. verified is True when version is a stable release
    (vMAJOR.MINOR.PATCH) the client can compare; development builds,
    git-describe builds, and release candidates are unverified and pass every
    check."""

    version: str | None
    features: tuple[str, ...] = field(default_factory=tuple)
    verified: bool = False


def parse_stable_version(version: str) -> tuple[int, int, int] | None:
    m = _STABLE_VERSION.match(version.strip())
    if not m:
        return None
    return int(m.group(1)), int(m.group(2)), int(m.group(3))


def server_status(version: str, features: list[str]) -> ServerStatus:
    return ServerStatus(version, tuple(features), parse_stable_version(version) is not None)


def check_minimum(status: ServerStatus, minimum: str) -> None:
    """Raises ServerTooOldError when a verified server is older than minimum."""
    if status.version is None:
        raise ServerTooOldError(None, minimum)
    have = parse_stable_version(status.version)
    need = parse_stable_version(minimum)
    if have is not None and need is not None and have < need:
        raise ServerTooOldError(status.version, minimum)


def check_operation(status: ServerStatus, operation: str, since: str | None) -> None:
    """Raises UnsupportedOperationError when a verified server is older than the operation."""
    if not since or not status.verified or status.version is None:
        return
    have = parse_stable_version(status.version)
    need = parse_stable_version(since)
    if have is not None and need is not None and have < need:
        raise UnsupportedOperationError(operation, since, status.version)


#: How long a probe answer is reused before the server is asked again.
PROBE_TTL_SECONDS = 300.0


@dataclass(frozen=True)
class ProbeEntry:
    """A probe answer, or a 402 answer to serverInfo (payment). A 402 is no
    verdict, but it is kept like an answer: a v0.3.10 gateway answers an
    anonymous serverInfo with its x402 challenge because the field is not on
    its public allowlist, and asking again on every call would answer the
    same."""

    status: ServerStatus | None
    payment: FrameWorksError | None
    checked_at: float


class ProbeCache:
    """Probe results by GraphQL URL, shared by every client in the process.
    An answer (or a 402) is reused for PROBE_TTL_SECONDS; a probe that failed
    for any other reason is not kept, so the next call probes again."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._entries: dict[str, ProbeEntry] = {}
        self._url_locks: dict[str, threading.Lock] = {}

    def get(self, url: str, now: float) -> ProbeEntry | None:
        """The entry of url, or None when there is none younger than the TTL."""
        with self._lock:
            entry = self._entries.get(url)
        if entry is None or now - entry.checked_at >= PROBE_TTL_SECONDS:
            return None
        return entry

    def put(self, url: str, entry: ProbeEntry) -> None:
        with self._lock:
            self._entries[url] = entry

    def url_lock(self, url: str) -> threading.Lock:
        """Serializes sync probes of one URL so concurrent threads probe once."""
        with self._lock:
            lock = self._url_locks.get(url)
            if lock is None:
                lock = self._url_locks[url] = threading.Lock()
            return lock


PROBES = ProbeCache()
