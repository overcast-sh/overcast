"""
lib/scenario/failures.py — the one failure-message builder, and the two
exception types the interpreter raises.

Debuggability is the whole cost of executing an IR instead of reading a
hand-written test: nobody can grep for the line that failed, because there is
no line. compat/model/README.md § Failure messages pays that cost explicitly
by fixing six fields every interpreter failure must carry, in order:

  1. group/test
  2. the operation
  3. the exact params JSON sent (after evaluating every expression)
  4. the assertion kind and, where one applies, the path
  5. expected vs actual
  6. the scenario file and the step index

:func:`failure_message` is the only place that string is built, and every
assertion, call and export failure goes through it — so a field cannot be
forgotten in one clause and present in another.
"""

from __future__ import annotations

import json
from typing import Any, Optional

# A resolved value is clipped in a message; the params JSON never is. The
# params are what a reader retypes to reproduce the call, so they have to be
# exact — a `listContains` "actual", by contrast, is a whole page of results
# and is only ever read for its first few entries.
_MAX_VALUE_CHARS = 500


class _Missing:
    """The value of a path that did not resolve. Distinct from ``None``,
    which is a JSON ``null`` a path *did* resolve to."""

    _instance: Optional["_Missing"] = None

    def __new__(cls) -> "_Missing":
        if cls._instance is None:
            cls._instance = super().__new__(cls)
        return cls._instance

    def __repr__(self) -> str:  # pragma: no cover - trivial
        return "<missing>"


MISSING = _Missing()


class ScenarioError(Exception):
    """A step could not be executed as written: an unresolvable ``$ref``, an
    export path the response does not carry, a malformed expression.

    Setup treats it as a setup failure (every test in the group skips);
    teardown skips the step and continues; a test reports it as a failure,
    carrying the same six fields as any assertion failure."""


class ScenarioFailure(AssertionError):
    """An assertion clause that did not hold.

    ``AssertionError`` rather than a bare ``Exception`` so it reads like every
    hand-written test's failure in the harness, and so nothing mistakes it for
    a transport error."""


def render(value: Any) -> str:
    """Render a value the way the message shows it: JSON where JSON can, a
    repr where it cannot (a ``datetime``, a ``bytes`` blob).

    ``sort_keys`` keeps a message byte-identical across runs, which is what
    makes three-run comparison meaningful."""
    if value is MISSING:
        return "<missing>"
    try:
        return json.dumps(value, sort_keys=True, default=repr)
    except (TypeError, ValueError):  # pragma: no cover - default=repr covers it
        return repr(value)


def render_clipped(value: Any, limit: int = _MAX_VALUE_CHARS) -> str:
    """:func:`render`, clipped. For a value a message shows for orientation
    rather than for retyping."""
    text = render(value)
    if len(text) <= limit:
        return text
    return text[:limit] + f"... ({len(text)} chars total)"


def failure_message(
    *,
    group: str,
    test: str,
    op: str,
    params: Any,
    assertion: str,
    path: Optional[str] = None,
    expected: str,
    actual: str,
    scenario_file: str,
    step: str,
) -> str:
    """Build the six-field message. Every field is mandatory except ``path``,
    which only some assertion kinds have.

    ``expected`` and ``actual`` arrive pre-formatted: the caller knows whether
    it is comparing a value, a list membership or an error code, and the
    builder should not have to.
    """
    parts = [
        f"{group}/{test}:",
        f"op={op}",
        f"params={render(params)}",
        f"assertion={assertion}",
    ]
    if path is not None:
        parts.append(f"path={path}")
    parts.append(f"expected={expected}")
    parts.append(f"actual={actual}")
    parts.append(f"at {scenario_file} {step}")
    return " ".join(parts)
