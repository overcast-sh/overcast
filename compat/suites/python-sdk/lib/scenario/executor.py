"""
lib/scenario/executor.py — making the calls a scenario describes.

The SDK is used exactly as production code would use it: ``boto3.client(svc)``
and ``getattr(client, xform_name(op))(**params)`` is boto3's ordinary public
API, which is the whole reason ``python-sdk`` gets an interpreter rather than
generated source (plan §3.2). There is no Overcast-specific code path here; the
endpoint override lives in ``lib/clients.py`` and is the only deviation from a
production client.

Everything that can fail funnels through :class:`Executor`, so that a call, an
unresolvable ``$ref`` and a missing export path all report with the same six
fields as an assertion failure.
"""

from __future__ import annotations

import threading
from dataclasses import dataclass, replace
from typing import Any, Callable, Optional

from botocore import xform_name

from ..clients import make_client
# _is_unimplemented is the harness's own 501 detection, and the interpreter has
# to defer to it rather than re-implement it: what counts as "the emulator has
# not implemented this" must be one rule for the whole suite, or a generated
# probe and a hand-written test would disagree about the same response.
from ..harness import TestContext, _is_unimplemented
from .expressions import evaluate, resolve_path
from .failures import MISSING, ScenarioError, ScenarioFailure, failure_message
from .loader import ScenarioGroup

# Where the group's context bag lives on the TestContext. The harness gives
# every group its own TestContext, and setup, tests and teardown of one group
# all receive that same object — which is exactly the lifetime the IR gives the
# context bag.
_BAG_KEY = "__scenario_context__"


@dataclass(frozen=True)
class StepRef:
    """Fields 1 and 6 of a failure message: which test failed, and where in
    the scenario file the step that failed is written.

    ``test`` is a test name for a test, and ``setup``/``teardown`` for the
    group-level phases, which have no test of their own."""

    group: str
    test: str
    file: str
    step: str

    def child(self, step: str) -> "StepRef":
        """A nested step — the clause inside an ``eventually``."""
        return replace(self, step=f"{self.step}.{step}")


class Executor:
    """Executes calls for one group, against one context bag."""

    def __init__(self, spec: ScenarioGroup, ctx: TestContext, clients: "ClientCache") -> None:
        self.spec = spec
        self.ctx = ctx
        self._clients = clients
        bag = ctx.get(_BAG_KEY)
        if bag is None:
            bag = {}
            ctx[_BAG_KEY] = bag
        self.context: dict[str, Any] = bag

    # ── expressions ─────────────────────────────────────────────────────────

    def evaluate(self, value: Any) -> Any:
        return evaluate(value, context=self.context, run_id=self.ctx.run_id,
                        group=self.spec.name)

    def params_for(self, call: dict, ref: StepRef, assertion: str) -> dict:
        """Evaluate a call's params, or fail naming the expression that could
        not be evaluated. The raw IR params are shown, because the evaluated
        ones are what does not exist."""
        try:
            return self.evaluate(call.get("params") or {})
        except ScenarioError as exc:
            raise ScenarioFailure(failure_message(
                group=ref.group, test=ref.test, op=call["op"],
                params=call.get("params") or {}, assertion=assertion,
                expected="every value expression resolves",
                actual=str(exc),
                scenario_file=ref.file, step=ref.step,
            )) from exc

    # ── calls ───────────────────────────────────────────────────────────────

    def invoke(self, call: dict, params: dict) -> dict:
        """The SDK call itself, with nothing wrapped around it.

        Params are passed as the IR gives them: the generator emits a value of
        each member's modeled kind (a string for a string member, a number for
        a numeric one), so botocore's own input validation is the only coercion
        in play — the same one a hand-written call gets. Verified against
        botocore's service models for the whole pilot corpus; if a future
        scenario ever did carry a string for a numeric member, botocore would
        reject it here rather than silently coerce, which is the right failure.
        """
        client = self._clients.get(self.ctx, self.spec.client["endpointPrefix"])
        return getattr(client, xform_name(call["op"]))(**params)

    def perform(self, call: dict, ref: StepRef, assertion: str, *,
                apply_exports: bool = True) -> tuple[dict, dict]:
        """Evaluate params, make the call, apply the call's exports. Returns
        ``(params, response)``.

        ``apply_exports=False`` is for a clause whose exports are conditional
        on the clause holding — a ``readback``'s are applied only when its
        checks pass, so that a failed attempt inside an ``eventually`` cannot
        leave a half-written context bag behind.

        An error that is the emulator answering 501 is re-raised unchanged, so
        the harness's unimplemented detection still sees a botocore
        ``ClientError`` and records ``unimplemented`` rather than ``fail`` —
        which is the whole point of the probe groups."""
        params = self.params_for(call, ref, assertion)
        try:
            response = self.invoke(call, params)
        except Exception as exc:
            if _is_unimplemented(exc):
                raise
            raise ScenarioFailure(failure_message(
                group=ref.group, test=ref.test, op=call["op"], params=params,
                assertion=assertion, expected="the call succeeds",
                actual=f"{type(exc).__name__}: {exc}",
                scenario_file=ref.file, step=ref.step,
            )) from exc
        if apply_exports:
            self.apply_export(call, params, response, ref, assertion)
        return params, response

    def attempt(self, call: dict, ref: StepRef, assertion: str
                ) -> tuple[dict, Optional[dict], Optional[Exception]]:
        """Like :meth:`perform`, but for a clause that *expects* an error:
        returns ``(params, response, error)`` with exactly one of the last two
        set. Exports are not applied — a call that was supposed to fail has
        nothing to export, and one that succeeded is about to fail its clause.
        """
        params = self.params_for(call, ref, assertion)
        try:
            return params, self.invoke(call, params), None
        except Exception as exc:  # noqa: BLE001 - the clause decides what it means
            if _is_unimplemented(exc):
                raise
            return params, None, exc

    # ── exports ─────────────────────────────────────────────────────────────

    def apply_export(self, call: dict, params: dict, response: dict, ref: StepRef,
                     assertion: str) -> None:
        """Write a call's ``export`` paths into the context bag. A path the
        response does not carry is a failure of the step that carries it,
        naming the path."""
        for name, path in (call.get("export") or {}).items():
            value = resolve_path(response, path)
            if value is MISSING:
                raise ScenarioFailure(failure_message(
                    group=ref.group, test=ref.test, op=call["op"], params=params,
                    assertion=assertion, path=path,
                    expected=f'the response carries {path}, to export as "{name}"',
                    actual="<missing>",
                    scenario_file=ref.file, step=ref.step,
                ))
            self.context[name] = value

    # ── message helper ──────────────────────────────────────────────────────

    def fail(self, *, ref: StepRef, op: str, params: Any, assertion: str,
             expected: str, actual: str, path: Optional[str] = None) -> ScenarioFailure:
        return ScenarioFailure(failure_message(
            group=ref.group, test=ref.test, op=op, params=params,
            assertion=assertion, path=path, expected=expected, actual=actual,
            scenario_file=ref.file, step=ref.step,
        ))


class ClientCache:
    """One boto3 client per (endpoint, region, service), shared across the
    groups of a run.

    Building a client parses botocore's service model, which is slow enough to
    matter across 34 organizations tests, and a botocore client is safe to call
    from several threads once built. Construction is not, hence the lock."""

    def __init__(self, factory: Callable[[str, str, str], Any] = make_client) -> None:
        self._factory = factory
        self._lock = threading.Lock()
        self._cache: dict[tuple[str, str, str], Any] = {}

    def get(self, ctx: TestContext, service: str) -> Any:
        key = (ctx.endpoint, ctx.region, service)
        with self._lock:
            client = self._cache.get(key)
            if client is None:
                client = self._factory(ctx.endpoint, ctx.region, service)
                self._cache[key] = client
            return client


def error_names(exc: Exception) -> list[str]:
    """Every name an SDK error reports itself under.

    The SDKs disagree about whether they surface the modeled shape name or the
    wire code, so compat/model/README.md § Assertions has an interpreter accept
    either against either. For boto3 that means: the exception class botocore
    minted (the shape name, when the code matched a modeled error), the parsed
    ``Error.Code`` (which for a JSON protocol comes from ``__type``, and which
    Overcast fills with SQS's legacy ``AWS.SimpleQueueService.*`` code), and the
    ``x-amzn-query-error`` header AWS uses for the same purpose."""
    names = [type(exc).__name__]
    response = getattr(exc, "response", None)
    if isinstance(response, dict):
        error = response.get("Error") or {}
        for key in ("Code", "__type"):
            value = error.get(key)
            if isinstance(value, str) and value:
                names.append(value.rsplit("#", 1)[-1])
        wire_type = response.get("__type")
        if isinstance(wire_type, str) and wire_type:
            names.append(wire_type.rsplit("#", 1)[-1])
        headers = (response.get("ResponseMetadata") or {}).get("HTTPHeaders") or {}
        query_error = headers.get("x-amzn-query-error")
        if isinstance(query_error, str) and query_error:
            names.append(query_error.split(";")[0])
    seen: list[str] = []
    for name in names:
        if name not in seen:
            seen.append(name)
    return seen


def describe_error(exc: Optional[Exception]) -> str:
    """The fifth field's "actual" half for an error clause."""
    if exc is None:
        return "<no error>"
    return f"{'/'.join(error_names(exc))}: {exc}"


def error_matches(exc: Exception, error: dict) -> bool:
    accepted = {error.get("shape"), error.get("code")} - {None}
    return any(name in accepted for name in error_names(exc))


__all__ = [
    "ClientCache",
    "Executor",
    "StepRef",
    "describe_error",
    "error_matches",
    "error_names",
]
