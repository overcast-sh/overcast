"""
lib/scenario/expressions.py — the IR's value expressions and its path syntax.

Both are closed and tiny by design (compat/model/README.md § Values, § Paths):
eight implementations have to agree on every value, so there are no
conditionals, no arithmetic and no scripting, and a path is `$` followed by
`.Member` and `[n]` segments and nothing else — not JSONPath.

Nothing here talks to boto3 or to the harness, which is what makes it the
part of the interpreter that unit tests can exercise exhaustively.
"""

from __future__ import annotations

import re
from typing import Any, Mapping, Sequence, Union

from .failures import MISSING, ScenarioError

# `.Member` or `[index]`. The member charset is the schema's Path pattern:
# member names are the modeled names, and a map key selected the same way may
# carry `-`, `:` or `/`.
_SEGMENT_RE = re.compile(r"\.([A-Za-z_][A-Za-z0-9_\-:/]*)|\[(0|[1-9][0-9]*)\]")

PathSegment = Union[str, int]


def parse_path(path: str) -> list[PathSegment]:
    """Split a response path into its segments. ``$`` alone is no segments."""
    if not path.startswith("$"):
        raise ScenarioError(f'path "{path}" does not start with $')
    segments: list[PathSegment] = []
    pos = 1
    while pos < len(path):
        m = _SEGMENT_RE.match(path, pos)
        if m is None:
            raise ScenarioError(f'path "{path}" is malformed at offset {pos}')
        member, index = m.group(1), m.group(2)
        segments.append(member if member is not None else int(index))
        pos = m.end()
    return segments


def resolve_path(root: Any, path: str) -> Any:
    """Resolve ``path`` against ``root``, returning :data:`MISSING` when any
    segment is absent.

    A structure member and a map key are selected identically — `.Name` on a
    dict — because that is what the IR says and what the response shape makes
    of them is not the interpreter's business. A string is never indexed: it
    is a scalar here, not a sequence."""
    value = root
    for segment in parse_path(path):
        if isinstance(segment, str):
            if not isinstance(value, Mapping) or segment not in value:
                return MISSING
            value = value[segment]
        else:
            if isinstance(value, (str, bytes)) or not isinstance(value, Sequence):
                return MISSING
            if segment >= len(value):
                return MISSING
            value = value[segment]
    return value


def evaluate(value: Any, *, context: Mapping[str, Any], run_id: str, group: str) -> Any:
    """Evaluate a value expression against the group's context bag.

    An object with exactly one ``$``-prefixed key is an expression; any other
    object is a structure or map whose values are values; an array is a list
    of values; a scalar is itself."""
    if isinstance(value, Mapping):
        dollar_keys = [k for k in value if isinstance(k, str) and k.startswith("$")]
        if dollar_keys and len(value) == 1:
            return _expression(dollar_keys[0], value[dollar_keys[0]],
                               context=context, run_id=run_id, group=group)
        if dollar_keys:
            # The schema forbids it, and `$lit` exists precisely so an object
            # whose keys start with `$` can still be written.
            raise ScenarioError(
                f"object mixes the expression key {dollar_keys[0]!r} with other "
                f"members {sorted(k for k in value if k not in dollar_keys)!r} "
                "— use $lit for an object whose keys start with $"
            )
        return {k: evaluate(v, context=context, run_id=run_id, group=group)
                for k, v in value.items()}
    if isinstance(value, list):
        return [evaluate(v, context=context, run_id=run_id, group=group) for v in value]
    return value


def _expression(key: str, arg: Any, *, context: Mapping[str, Any], run_id: str,
                group: str) -> Any:
    if key == "$lit":
        # Verbatim, never interpreted — not even one level down.
        return arg
    if key == "$ref":
        if arg not in context:
            raise ScenarioError(
                f'unresolvable $ref "{arg}": the group context holds '
                f"{sorted(context)!r}"
            )
        return context[arg]
    if key == "$name":
        # {runId}-{group}-{suffix}, with the whole group name as the token and
        # no shortening anywhere: that is what makes the name-hygiene rule hold
        # by construction, and what lets the orphan sweep find a leak.
        return f"{run_id}-{group}-{arg}"
    if key == "$concat":
        parts: list[str] = []
        for part in arg:
            evaluated = evaluate(part, context=context, run_id=run_id, group=group)
            if not isinstance(evaluated, str):
                raise ScenarioError(
                    f"$concat part {part!r} evaluated to {evaluated!r}, which is "
                    "not a string"
                )
            parts.append(evaluated)
        return "".join(parts)
    if key == "$index":
        target, index = arg[0], arg[1]
        evaluated = evaluate(target, context=context, run_id=run_id, group=group)
        if isinstance(evaluated, (str, bytes)) or not isinstance(evaluated, Sequence):
            raise ScenarioError(f"$index target evaluated to {evaluated!r}, which is not a list")
        if index >= len(evaluated):
            raise ScenarioError(
                f"$index {index} is out of range for a list of {len(evaluated)}"
            )
        return evaluated[index]
    raise ScenarioError(f"unknown value expression {key!r}")


def json_equal(a: Any, b: Any) -> bool:
    """Equality "as JSON", per compat/model/README.md.

    The SDK has already done its own mapping by the time we see a value: a
    boto3 ``int`` is a JSON number, a ``bool`` a boolean, a ``dict`` an object.
    The generator only ever emits an ``equals`` literal of the member's modeled
    kind, so **no coercion is applied** — a string is never parsed into a
    number, and a number is never formatted into a string. The one thing
    Python would get wrong on its own is ``True == 1``, which is false as JSON,
    so booleans are compared identically and never against numbers. Two
    numbers of different Python types (``1`` and ``1.0``) are equal, because
    JSON has one number type. Timestamps and blobs are never compared."""
    if isinstance(a, bool) or isinstance(b, bool):
        return isinstance(a, bool) and isinstance(b, bool) and a is b
    if isinstance(a, (int, float)) and isinstance(b, (int, float)):
        return a == b
    if isinstance(a, Mapping) and isinstance(b, Mapping):
        return a.keys() == b.keys() and all(json_equal(a[k], b[k]) for k in a)
    if isinstance(a, list) and isinstance(b, list):
        return len(a) == len(b) and all(json_equal(x, y) for x, y in zip(a, b))
    if isinstance(a, Mapping) != isinstance(b, Mapping):
        return False
    if isinstance(a, list) != isinstance(b, list):
        return False
    return a == b


def is_non_empty(value: Any) -> bool:
    """``nonEmpty``: not :data:`MISSING`, and not ``null``, ``""``, ``[]`` or
    ``{}``. Numbers and booleans are never empty — ``0`` and ``False`` pass."""
    if value is MISSING or value is None:
        return False
    if isinstance(value, bool) or isinstance(value, (int, float)):
        return True
    if isinstance(value, (str, bytes, list, tuple, dict, set)):
        return len(value) > 0
    return True
