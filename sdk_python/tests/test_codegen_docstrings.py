"""The ariadne-codegen plugin documents each client method with its target
field's description and, for an operation on an @experimental field, the
experimental note from pkg/graphql/public/generated/targets.json."""

from __future__ import annotations

import sys
from pathlib import Path
from typing import Any

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "codegen"))

import forward_compat  # noqa: E402

NOTE = (
    "Experimental until v0.4.0: Counting is sampled. "
    "A later SDK release of this line may change or remove this operation."
)


@pytest.fixture
def targets_file(monkeypatch: pytest.MonkeyPatch) -> None:
    data: dict[str, Any] = {
        "targets": {"GetViewerCount": "query.viewerCount", "GetStream": "query.stream"},
        "experimental": {
            "GetViewerCount": {"until": "v0.4.0", "reason": "Counting is sampled", "note": NOTE}
        },
    }
    monkeypatch.setattr(forward_compat, "_targets_file", lambda: data)


def test_experimental_operation_docstring_carries_the_note(targets_file: None) -> None:
    note = forward_compat._experimental_note("GetViewerCount")
    assert note == NOTE
    assert (
        forward_compat._method_docstring("The current viewer count.", note)
        == f"The current viewer count.\n\n{NOTE}"
    )
    assert forward_compat._method_docstring(None, note) == NOTE


def test_stable_operation_docstring_is_the_description(targets_file: None) -> None:
    note = forward_compat._experimental_note("GetStream")
    assert note is None
    assert forward_compat._method_docstring("One stream.", note) == "One stream."


def test_repository_targets_file_lists_experimental_operations() -> None:
    forward_compat._targets_file.cache_clear()
    assert isinstance(forward_compat._targets_file().get("experimental"), dict)
