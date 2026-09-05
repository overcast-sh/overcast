"""
lib/scenario/loader.py — reading the scenario IR files the registry names.

A generated registry group carries a ``scenario`` field: a repository-relative
path such as ``compat/model/scenarios/sqs.json``. It is resolved from the same
base the loader resolves ``registry.json`` from — never from the current
working directory, because the suite is run from the repo root by
``cmd/compat``, from ``compat/suites/python-sdk`` by hand, and from ``/`` in a
container.

Files are read lazily and once: the pilot is two files, but a service is only
opened when a group of that service is actually built.
"""

from __future__ import annotations

import json
import os
import sys
import threading
from dataclasses import dataclass
from typing import Any, Optional

from ..registry import REPO_ROOT


@dataclass(frozen=True)
class ScenarioGroup:
    """One group of one scenario file, with the client spec it is executed
    against and the file it came from (for failure messages)."""

    file: str  # repository-relative, exactly as the registry spells it
    service: str
    client: dict
    name: str
    kind: str
    setup: list[dict]
    tests: dict[str, dict]
    teardown: list[dict]


class ScenarioLibrary:
    """Lazily loads scenario files and indexes their groups by name.

    Thread-safe: groups are resolved while the registry is being built (one
    thread), but the cache is shared with nothing that would notice a race
    either way, and a lock costs nothing here."""

    def __init__(self, base_dir: str = REPO_ROOT) -> None:
        self._base_dir = base_dir
        self._lock = threading.Lock()
        self._files: dict[str, Optional[dict[str, ScenarioGroup]]] = {}

    def group(self, scenario_file: Optional[str], group_name: str) -> Optional[ScenarioGroup]:
        """The named group of that scenario file, or None when there is no
        such file or no such group — "not mine", which is what the backend
        hook returns for anything it cannot resolve."""
        if not scenario_file:
            return None
        with self._lock:
            groups = self._load(scenario_file)
        if groups is None:
            return None
        return groups.get(group_name)

    def _load(self, scenario_file: str) -> Optional[dict[str, ScenarioGroup]]:
        if scenario_file in self._files:
            return self._files[scenario_file]

        path = os.path.join(self._base_dir, *scenario_file.split("/"))
        groups: Optional[dict[str, ScenarioGroup]]
        try:
            with open(path, encoding="utf-8") as f:
                data = json.load(f)
            groups = _index(scenario_file, data)
        except (OSError, ValueError, KeyError, TypeError) as exc:
            # Not fatal, and not silent. The registry and the scenario files
            # are generated together, so this cannot happen in a healthy tree;
            # when it does, returning None lets the loader's own sentinel
            # report a loud failure for every test of the group rather than
            # aborting the whole suite over one unreadable file.
            sys.stderr.write(
                f"[compat:python-sdk] cannot read scenario file {path}: {exc}\n"
            )
            groups = None

        self._files[scenario_file] = groups
        return groups


def _index(scenario_file: str, data: Any) -> dict[str, ScenarioGroup]:
    version = data["version"]
    if version != 1:
        raise ValueError(f"unsupported scenario version {version!r} (want 1)")
    service = data["service"]
    client = data["client"]
    indexed: dict[str, ScenarioGroup] = {}
    for group in data["groups"]:
        indexed[group["name"]] = ScenarioGroup(
            file=scenario_file,
            service=service,
            client=client,
            name=group["name"],
            kind=group["kind"],
            setup=list(group.get("setup") or []),
            tests={t["name"]: t for t in group["tests"]},
            teardown=list(group.get("teardown") or []),
        )
    return indexed
