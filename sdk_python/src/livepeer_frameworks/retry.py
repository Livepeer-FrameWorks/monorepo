"""When and how long the client waits before sending a failed request again."""

from __future__ import annotations

import random
import re
import time
from dataclasses import dataclass
from email.utils import parsedate_to_datetime


@dataclass(frozen=True)
class RetryPolicy:
    """max_attempts counts attempts in total, including the first (1 disables
    retries). The first retry waits base_delay_ms and each later one doubles
    it, up to max_delay_ms. A Retry-After longer than max_retry_after_ms ends
    retrying instead of waiting. jitter randomizes each wait between half and
    all of it, so clients do not retry in step."""

    max_attempts: int = 3
    base_delay_ms: int = 250
    max_delay_ms: int = 4000
    max_retry_after_ms: int = 30_000
    jitter: bool = True


DEFAULT_RETRY_POLICY = RetryPolicy()

# Timeouts, throttling, and transient server failures.
RETRYABLE_STATUSES = frozenset({408, 429, 500, 502, 503, 504})


def backoff_delay_ms(policy: RetryPolicy, retry: int) -> int:
    """The wait before retry number `retry` (1 for the first retry)."""
    delay: int = min(policy.max_delay_ms, policy.base_delay_ms << (retry - 1))
    if not policy.jitter:
        return delay
    return round(delay / 2 + random.random() * (delay / 2))


_DELTA_SECONDS = re.compile(r"^\d+$")


def parse_retry_after(value: str | None, now: float | None = None) -> int | None:
    """Parses a Retry-After value (delta seconds or an HTTP date) into whole
    seconds; None when it is missing or unparseable."""
    if value is None:
        return None
    trimmed = value.strip()
    if _DELTA_SECONDS.match(trimmed):
        return int(trimmed)
    try:
        when = parsedate_to_datetime(trimmed)
    except (TypeError, ValueError):
        return None
    current = time.time() if now is None else now
    return max(0, int(-(-(when.timestamp() - current) // 1)))
