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

import base64
import binascii
import json
import math
import re
import urllib.parse
from typing import Any, Mapping, Optional, Sequence, Union

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


def evaluate(value: Any, *, context: Mapping[str, Any], run_id: str, group: str,
             now_ms: Optional[int] = None) -> Any:
    """Evaluate a value expression against the group's context bag.

    An object with exactly one ``$``-prefixed key is an expression; any other
    object is a structure or map whose values are values; an array is a list
    of values; a scalar is itself.

    ``now_ms`` is the clock, in epoch milliseconds, read once for the call
    whose params these are (see :func:`now_value`). It is None everywhere else
    — an expected value, a ``where`` — and a ``$now`` evaluated there fails."""
    if isinstance(value, Mapping):
        dollar_keys = [k for k in value if isinstance(k, str) and k.startswith("$")]
        if dollar_keys and len(value) == 1:
            return _expression(dollar_keys[0], value[dollar_keys[0]],
                               context=context, run_id=run_id, group=group, now_ms=now_ms)
        if dollar_keys:
            # The schema forbids it, and `$lit` exists precisely so an object
            # whose keys start with `$` can still be written.
            raise ScenarioError(
                f"object mixes the expression key {dollar_keys[0]!r} with other "
                f"members {sorted(k for k in value if k not in dollar_keys)!r} "
                "— use $lit for an object whose keys start with $"
            )
        return {k: evaluate(v, context=context, run_id=run_id, group=group, now_ms=now_ms)
                for k, v in value.items()}
    if isinstance(value, list):
        return [evaluate(v, context=context, run_id=run_id, group=group, now_ms=now_ms) for v in value]
    return value


def _expression(key: str, arg: Any, *, context: Mapping[str, Any], run_id: str,
                group: str, now_ms: Optional[int]) -> Any:
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
            evaluated = evaluate(part, context=context, run_id=run_id, group=group, now_ms=now_ms)
            if not isinstance(evaluated, str):
                raise ScenarioError(
                    f"$concat part {part!r} evaluated to {evaluated!r}, which is "
                    "not a string"
                )
            parts.append(evaluated)
        return "".join(parts)
    if key == "$index":
        target, index = arg[0], arg[1]
        evaluated = evaluate(target, context=context, run_id=run_id, group=group, now_ms=now_ms)
        if isinstance(evaluated, (str, bytes)) or not isinstance(evaluated, Sequence):
            raise ScenarioError(f"$index target evaluated to {evaluated!r}, which is not a list")
        if index >= len(evaluated):
            raise ScenarioError(
                f"$index {index} is out of range for a list of {len(evaluated)}"
            )
        return evaluated[index]
    if key == "$base64":
        # A blob: these bytes. boto3 takes ``bytes`` for a blob member and
        # would send a ``str`` as its UTF-8 — which is why a blob is never a
        # plain string in the IR. The argument is base64 text: a literal, or a
        # $ref to a blob a previous call exported, which the context bag holds
        # in its document form (see :func:`to_document`).
        text = evaluate(arg, context=context, run_id=run_id, group=group, now_ms=now_ms)
        return decode_base64(text)
    if key == "$now":
        return now_value(arg, now_ms)
    raise ScenarioError(f"unknown value expression {key!r}")


# `$now`'s closed set of units, and the bound on its offset either way: one
# hour (compat/model/README.md § Values).
NOW_UNITS = frozenset({"epochMillis"})
NOW_MAX_OFFSET_MILLIS = 3_600_000


def now_offset(arg: Any) -> int:
    """Validate a ``$now`` argument and return its offset in milliseconds.
    compat/model/testdata/now pins what every backend accepts and refuses."""
    if not isinstance(arg, Mapping):
        raise ScenarioError(f'$now takes {{"unit": "epochMillis"}}, got {arg!r}')
    unknown = sorted(set(arg) - {"unit", "offsetMillis"})
    if unknown:
        raise ScenarioError(f"$now has no member {unknown[0]!r}; it takes unit and offsetMillis")
    if "unit" not in arg:
        raise ScenarioError('$now needs "unit": "epochMillis"')
    offset = arg.get("offsetMillis", None)
    if "offsetMillis" in arg:
        # A bool is an int to Python and never a number to JSON.
        if isinstance(offset, bool) or not isinstance(offset, int):
            raise ScenarioError(
                f"$now offsetMillis must be a whole number of milliseconds, got {offset!r}")
        if offset == 0:
            raise ScenarioError("$now offsetMillis is 0; the scenario omits it instead")
    return check_now_arguments(arg["unit"], offset or 0)


def check_now_arguments(unit: Any, offset: int) -> int:
    """The two things any ``$now`` comes down to — a unit the IR has, and an
    offset inside an hour — held to the same rule every runtime holds its
    ``Now(unit, offsetMillis)`` to."""
    if unit not in NOW_UNITS:
        raise ScenarioError(f'$now unit {unit!r} is not one the IR has; its one unit is "epochMillis"')
    if not -NOW_MAX_OFFSET_MILLIS <= offset <= NOW_MAX_OFFSET_MILLIS:
        raise ScenarioError(f"$now offsetMillis {offset} is outside ±{NOW_MAX_OFFSET_MILLIS} (one hour)")
    return offset


def now_value(arg: Any, now_ms: Optional[int]) -> int:
    """``$now``: the client's clock when the call is made, in epoch
    milliseconds, plus the offset. The executor reads the clock once per call
    and hands the reading down, so every ``$now`` in one call's params sees the
    same instant and their offsets order them. There is no reading anywhere
    else: an expected value is compared with a response, and no response holds
    the instant a call was made at."""
    offset = now_offset(arg)
    if now_ms is None:
        raise ScenarioError(
            "$now is read when a call is made, so it can only be a call's param, "
            "never an expected value")
    return now_ms + offset


def decode_base64(text: Any) -> bytes:
    """Decode a blob's document form: standard base64 with padding, in its one
    canonical spelling. ``b64decode(validate=True)`` refuses a character
    outside the alphabet but not non-zero trailing bits, so the round trip is
    what refuses a second spelling of the same bytes. compat/model/testdata/
    blobs pins what every backend accepts and refuses."""
    if not isinstance(text, str):
        raise ScenarioError(f"$base64 takes base64 text, got {text!r}")
    try:
        raw = base64.b64decode(text, validate=True)
    except (binascii.Error, ValueError) as exc:
        raise ScenarioError(f"$base64 {text!r} is not standard padded base64: {exc}") from exc
    canonical = base64.b64encode(raw).decode("ascii")
    if canonical != text:
        raise ScenarioError(
            f"$base64 {text!r} is not the canonical spelling of its bytes, which is {canonical!r}"
        )
    return raw


def to_document(value: Any) -> Any:
    """Map a boto3 response to the IR's document form, which differs from what
    boto3 returns in one respect: a blob is ``bytes`` in boto3 and its standard
    base64 text in the document, as it is in every other backend (the AWS CLI
    prints it that way, and the typed suites render their SDK's blob type the
    same). That is what an ``equals`` against a ``$base64`` compares, and what
    an export of a blob puts in the context bag for a later ``$base64`` around a
    ``$ref`` to decode. Everything else — a ``datetime``, a streaming body — is
    left as boto3 gave it."""
    if isinstance(value, (bytes, bytearray)):
        return base64.b64encode(bytes(value)).decode("ascii")
    if isinstance(value, dict):
        return {k: to_document(v) for k, v in value.items()}
    if isinstance(value, list):
        return [to_document(v) for v in value]
    return value


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
    JSON has one number type. A blob compares as its document form, base64
    text, whichever side it is on: a ``$base64`` evaluates to ``bytes`` and a
    response blob is already text. Timestamps are never compared."""
    if isinstance(a, (bytes, bytearray)):
        a = to_document(a)
    if isinstance(b, (bytes, bytearray)):
        b = to_document(b)
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


# ── equalsJSON ───────────────────────────────────────────────────────────────
#
# compat/model/README.md § Documents in a string: a member whose content is a
# JSON document is compared as a document, not as text, because botocore hands
# this suite an IAM policy already decoded to a dict while five other backends
# see the percent-encoded string. compat/model/testdata/equalsjson pins it.


def check_equals_json_operand(operand: Any) -> None:
    """Refuse an ``equalsJSON`` operand that is not a literal JSON object or
    array, or that has a ``$``-prefixed key at any depth. The operand is never
    evaluated, so such a key could only be read one way or the other by
    guessing; it is refused instead."""
    if isinstance(operand, bool) or not isinstance(operand, (Mapping, list)):
        raise ScenarioError(
            f"equalsJSON takes a JSON object or array, got {operand!r}")
    _refuse_dollar_keys(operand)


def _refuse_dollar_keys(value: Any) -> None:
    if isinstance(value, Mapping):
        for key, member in value.items():
            if isinstance(key, str) and key.startswith("$"):
                raise ScenarioError(
                    f"equalsJSON's operand is a literal document and is never "
                    f"evaluated, so it may not hold the key {key!r}")
            _refuse_dollar_keys(member)
    elif isinstance(value, list):
        for member in value:
            _refuse_dollar_keys(member)


def percent_decode(text: str) -> str:
    """``%XX`` becomes that byte, ``+`` stays ``+``, a ``%`` not followed by
    two hex digits is kept, and the bytes are read as UTF-8 — once. This is
    exactly ``urllib.parse.unquote``, which is what botocore's
    ``json_decode_policies`` applies, so it is used rather than restated."""
    return urllib.parse.unquote(text, encoding="utf-8", errors="replace")


def _refuse_constant(name: str) -> Any:
    # `json` reads NaN and Infinity by default; neither is JSON.
    raise ValueError(f"{name} is not JSON")


def as_json_document(value: Any) -> tuple[bool, Any]:
    """The document the value at an ``equalsJSON`` path holds, as ``(True,
    document)``, or ``(False, None)`` when it holds none.

    A string is percent-decoded once and parsed as one JSON text; ``json.loads``
    already allows JSON whitespace around it and refuses anything else after
    it. A dict or a list is the document already — botocore decoded it. Any
    other value (a number, a boolean, ``None``, :data:`MISSING`) is not one."""
    if isinstance(value, str):
        try:
            return True, json.loads(percent_decode(value), parse_constant=_refuse_constant)
        except ValueError:
            return False, None
    if isinstance(value, (Mapping, list)):
        return True, value
    return False, None


def json_document_equal(a: Any, b: Any) -> bool:
    """Two documents are the same JSON value. Unlike :func:`json_equal` this
    compares numbers as IEEE-754 doubles, as every other backend does, rather
    than with Python's exact int/float comparison: ``2**53 + 1`` and
    ``2**53`` are one double. A ``bool`` is an ``int`` to Python and never a
    number to JSON."""
    if isinstance(a, bool) or isinstance(b, bool):
        return isinstance(a, bool) and isinstance(b, bool) and a is b
    if isinstance(a, (int, float)) and isinstance(b, (int, float)):
        return _as_double(a) == _as_double(b)
    if isinstance(a, Mapping) and isinstance(b, Mapping):
        return a.keys() == b.keys() and all(json_document_equal(a[k], b[k]) for k in a)
    if isinstance(a, list) and isinstance(b, list):
        return len(a) == len(b) and all(json_document_equal(x, y) for x, y in zip(a, b))
    if isinstance(a, str) and isinstance(b, str):
        return a == b
    return a is None and b is None


def _as_double(n: Any) -> float:
    try:
        return float(n)
    except OverflowError:
        # An int past the largest double is what a JSON parser that reads
        # doubles would make infinite.
        return math.inf if n > 0 else -math.inf


def compact_json(document: Any) -> str:
    """A document as the failure message shows it: compact, members sorted."""
    return json.dumps(document, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


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
