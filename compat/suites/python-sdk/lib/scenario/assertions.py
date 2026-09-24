"""
lib/scenario/assertions.py — the closed assertion set.

compat/model/README.md § Assertions is the normative table; this module is one
function per row and nothing else. Every failure goes through
``Executor.fail``, so all six required fields are present in every message by
construction rather than by remembering.
"""

from __future__ import annotations

import re
import time
from dataclasses import dataclass
from typing import Any, Optional

from .executor import Executor, StepRef, describe_error, error_matches
from .expressions import (
    as_json_document,
    check_equals_json_operand,
    compact_json,
    is_non_empty,
    json_document_equal,
    json_equal,
    resolve_path,
)
from .failures import MISSING, ScenarioError, ScenarioFailure, clip, render, render_clipped


@dataclass
class Primary:
    """The test's own call: what a clause with no ``call`` of its own reads,
    and what fields 2 and 3 name for such a clause."""

    op: str
    params: dict
    response: Optional[dict]
    error: Optional[Exception]


def check_clause(ex: Executor, clause: dict, ref: StepRef, primary: Primary) -> None:
    """Evaluate one assertion clause. Returns on success; raises
    :class:`ScenarioFailure` with the six-field message otherwise."""
    kind = clause["kind"]
    if kind == "responseField":
        return _response_field(ex, clause, ref, primary)
    if kind == "readback":
        return _readback(ex, clause, ref)
    if kind == "listContains":
        return _list_membership(ex, clause, ref, primary, want_member=True)
    if kind == "absent":
        if "error" in clause:
            return _absent_error(ex, clause, ref)
        return _list_membership(ex, clause, ref, primary, want_member=False)
    if kind == "errorCode":
        return _error_code(ex, clause, ref, primary)
    if kind == "eventually":
        return _eventually(ex, clause, ref, primary)
    raise ScenarioFailure(
        f"{ref.group}/{ref.test}: unknown assertion kind {kind!r} "
        f"at {ref.file} {ref.step}"
    )


# ── responseField ────────────────────────────────────────────────────────────

def _response_field(ex: Executor, clause: dict, ref: StepRef, primary: Primary) -> None:
    if primary.response is None:
        # The generator refuses a test that both expects an error and reads its
        # own response, so this is a corrupt scenario file rather than a
        # failing emulator.
        raise ex.fail(
            ref=ref, op=primary.op, params=primary.params, assertion="responseField",
            expected="the test's own response, to check against",
            actual=describe_error(primary.error),
        )
    _run_checks(ex, clause["checks"], primary.response, ref, "responseField",
                primary.op, primary.params)


# ── readback ─────────────────────────────────────────────────────────────────

def _readback(ex: Executor, clause: dict, ref: StepRef) -> None:
    call = clause["call"]
    # Exports are deferred: a readback's exports are applied only when its
    # checks pass, so a failed attempt inside an `eventually` leaves the
    # context bag exactly as it found it.
    params, response = ex.perform(call, ref, "readback", apply_exports=False)
    _run_checks(ex, clause["checks"], response, ref, "readback", call["op"], params)
    ex.apply_export(call, params, response, ref, "readback")


# ── listContains / absent (list form) ────────────────────────────────────────

def _list_membership(ex: Executor, clause: dict, ref: StepRef, primary: Primary,
                     *, want_member: bool) -> None:
    kind = "listContains" if want_member else "absent"
    call = clause.get("call")
    if call is not None:
        params, response = ex.perform(call, ref, kind, apply_exports=False)
        op = call["op"]
    else:
        if primary.response is None:
            raise ex.fail(
                ref=ref, op=primary.op, params=primary.params, assertion=kind,
                expected="the test's own response, to read the list from",
                actual=describe_error(primary.error),
            )
        params, response, op = primary.params, primary.response, primary.op

    items_path = clause["itemsPath"]
    items = resolve_path(response, items_path)
    try:
        where = {path: ex.evaluate(expected)
                 for path, expected in clause["where"].items()}
    except ScenarioError as exc:
        raise ex.fail(
            ref=ref, op=op, params=params, assertion=kind, path=items_path,
            expected=f"the where clause {render(clause['where'])}, once evaluated",
            actual=str(exc),
        ) from exc
    criteria = ", ".join(f"{path}={render(value)}" for path, value in where.items())

    if items is MISSING:
        # A missing list counts as empty: several AWS services omit the member
        # rather than serialize []. Empty satisfies `absent` and fails
        # `listContains`, which needs a non-empty list containing the item.
        items = []
    elif not isinstance(items, list):
        raise ex.fail(
            ref=ref, op=op, params=params, assertion=kind, path=items_path,
            expected="a list", actual=render_clipped(items),
        )

    matched = [item for item in items if _matches(item, where)]

    if want_member and not matched:
        raise ex.fail(
            ref=ref, op=op, params=params, assertion=kind, path=items_path,
            expected=f"a non-empty list containing an item where {criteria}",
            actual=(f"{len(items)} item(s): {render_clipped(items)}" if items
                    else "an empty or absent list"),
        )
    if not want_member and matched:
        raise ex.fail(
            ref=ref, op=op, params=params, assertion=kind, path=items_path,
            expected=f"no item where {criteria}",
            actual=f"{len(matched)} matching item(s): {render_clipped(matched)}",
        )

    if call is not None:
        ex.apply_export(call, params, response, ref, kind)


def _matches(item: Any, where: dict[str, Any]) -> bool:
    """An item matches when every ``where`` entry is equal, as JSON. ``$`` is
    the item itself, which is how a list of bare strings is matched."""
    for path, expected in where.items():
        if not json_equal(resolve_path(item, path), expected):
            return False
    return True


# ── absent (error form) / errorCode ──────────────────────────────────────────

def _absent_error(ex: Executor, clause: dict, ref: StepRef) -> None:
    call = clause["call"]
    params, _response, error = ex.attempt(call, ref, "absent")
    if error is not None and error_matches(error, clause["error"]):
        return
    raise ex.fail(
        ref=ref, op=call["op"], params=params, assertion="absent",
        expected=_accepted(clause["error"]), actual=describe_error(error),
    )


def _error_code(ex: Executor, clause: dict, ref: StepRef, primary: Primary) -> None:
    if primary.error is not None and error_matches(primary.error, clause["error"]):
        return
    raise ex.fail(
        ref=ref, op=primary.op, params=primary.params, assertion="errorCode",
        expected=_accepted(clause["error"]), actual=describe_error(primary.error),
    )


def _accepted(error: dict) -> str:
    """The fifth field's "expected" half: the codes the clause accepts. Either
    of the two, matched against either the SDK's reported code or its type
    name, because the SDKs disagree about which they surface."""
    codes = sorted({error.get("shape"), error.get("code")} - {None})
    return "an error whose code or type name is one of " + ", ".join(codes)


# ── eventually ───────────────────────────────────────────────────────────────

def _eventually(ex: Executor, clause: dict, ref: StepRef, primary: Primary) -> None:
    """Retry the inner clause up to ``maxAttempts`` times, ``delayMs`` apart.

    The bounded poll loop is the sanctioned alternative to a fixed sleep: it
    only exists where the recipe declared the resource eventually consistent,
    and the budget is the recipe author's. The last attempt's failure is the
    one reported, because it is the state the service actually settled in."""
    max_attempts = clause["maxAttempts"]
    delay_s = clause.get("delayMs", 0) / 1000.0
    inner = clause["assert"]
    inner_ref = ref.child("assert")
    last: Optional[ScenarioFailure] = None
    for attempt in range(1, max_attempts + 1):
        try:
            check_clause(ex, inner, inner_ref, primary)
            return
        except ScenarioFailure as exc:
            last = exc
            if attempt < max_attempts and delay_s > 0:
                time.sleep(delay_s)
    raise ScenarioFailure(
        f"eventually gave up after {max_attempts} attempt(s) "
        f"{int(delay_s * 1000)}ms apart; last failure: {last}"
    )


# ── checks ───────────────────────────────────────────────────────────────────

def _run_checks(ex: Executor, checks: dict, response: dict, ref: StepRef,
                assertion: str, op: str, params: dict) -> None:
    for path, check in checks.items():
        value = resolve_path(response, path)
        name, argument = next(iter(check.items()))
        try:
            expected, holds, actual = _check(name, argument, value, ex)
        except ScenarioError as exc:
            # An `equals` whose expected value is a $ref nothing exported, or
            # an `equalsJSON` operand the loader should already have refused.
            raise ex.fail(
                ref=ref, op=op, params=params, assertion=assertion, path=path,
                expected=f"{name} {render(argument)}, once evaluated",
                actual=str(exc),
            ) from exc
        except re.error as exc:
            # A `matches` pattern the model states in RE2 that `re` will not
            # compile. It is a normal mismatch with the same six fields as any
            # other, never a raw exception out of the evaluator: the run must
            # report which test and which pattern, not a traceback.
            raise ex.fail(
                ref=ref, op=op, params=params, assertion=assertion, path=path,
                expected=f"pattern {argument}",
                actual=f"unsupported pattern: {exc}",
            ) from exc
        if not holds:
            raise ex.fail(
                ref=ref, op=op, params=params, assertion=assertion, path=path,
                expected=expected,
                actual=render_clipped(value) if actual is None else actual,
            )


def _check(name: str, argument: Any, value: Any,
           ex: Executor) -> tuple[str, bool, Optional[str]]:
    """One check: its "expected" description, whether it holds, and its own
    "actual" description when the plain rendering of the value is not it."""
    if name == "nonEmpty":
        return "a value that is not null, \"\", [] or {}", is_non_empty(value), None
    if name == "isList":
        # True of a list, empty or not, and true of a member the service
        # omitted rather than serializing as []. A present non-list fails.
        return ("a list, empty or absent",
                value is MISSING or isinstance(value, list), None)
    if name == "equals":
        expected = ex.evaluate(argument)
        return f"equals {render(expected)}", json_equal(value, expected), None
    if name == "equalsJSON":
        return _equals_json(argument, value)
    if name == "matches":
        # Compiled before the value is looked at, so a pattern `re` rejects is
        # reported as an unsupported pattern whatever the response held.
        pattern = re.compile(argument)
        return (f"matches /{argument}/",
                isinstance(value, str) and pattern.search(value) is not None, None)
    if name == "missing":
        return "the path not to resolve", value is MISSING, None
    raise ScenarioFailure(f"unknown check {name!r}")


def _equals_json(operand: Any, value: Any) -> tuple[str, bool, Optional[str]]:
    """``equalsJSON``: the value is a JSON document equal to the operand.

    The operand is never evaluated — it is a literal document. botocore has
    already decoded an IAM policy member into a dict, which is the document as
    it stands; a string is percent-decoded once and parsed. The actual side is
    the decoded document, so a reader compares two documents rather than a
    percent-encoded string with an object."""
    check_equals_json_operand(operand)
    expected = f"equalsJSON {compact_json(operand)}"
    if value is MISSING:
        return expected, False, render(value)
    is_document, document = as_json_document(value)
    if not is_document:
        return expected, False, f"not a JSON document: {render_clipped(value)}"
    return (expected, json_document_equal(document, operand),
            f"document {clip(compact_json(document))}")
