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


# The whole of compat/model/README.md § Naming's python-sdk row: botocore's
# service name is the scenario's endpoint prefix except for four services,
# where botocore keeps a shorter historical directory name. It is a table
# because nothing about "elasticloadbalancing" implies "elb" — botocore's
# service names come from its own data directory, not from the endpoint, and
# for these four the two disagree. ``boto3.client("elasticloadbalancing")``
# raises UnknownServiceError, so a generated group for elastic-load-balancing
# would report every test as a failure of the service rather than of the
# derivation.
#
# The plan (§7.3) asked for this to land with the first scenario that names one
# of the four rather than up front, which is ``elastic-load-balancing``; the
# other three are here because they are one documented set and a table with one
# entry invites the next author to add theirs somewhere else.
_BOTOCORE_SERVICE_OVERRIDES = {
    "elasticloadbalancing": "elb",
    "monitoring": "cloudwatch",
    "email": "ses",
    "states": "stepfunctions",
}


def botocore_service(endpoint_prefix: str) -> str:
    """The botocore service name for a scenario's endpoint prefix."""
    return _BOTOCORE_SERVICE_OVERRIDES.get(endpoint_prefix, endpoint_prefix)


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
        client = self._clients.get(self.ctx, botocore_service(self.spec.client["endpointPrefix"]))
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
    wire code, so compat/model/README.md § Errors has an interpreter accept
    either against either, over a fixed list of surfaces. This reads the three
    boto3 actually has:

    * the exception class botocore minted, which is the modeled shape's name
      whenever ``Error.Code`` matched a modeled error's code;
    * ``Error.Code`` — one field, wherever the code came from. botocore
      resolves it from the body's ``__type`` (namespace already stripped), from
      ``x-amzn-errortype`` or the body's ``code``/``Code`` member for REST JSON,
      from the ``Code`` inside the XML error node for AWS Query and REST XML —
      ``<ErrorResponse><Error><Code>`` and the bare ``<Error><Code>`` alike,
      which is the nested half of the Errors table's body-code row and the only
      place a Query service ever states its code — and replaces it with the
      ``x-amzn-query-error`` code for a query-compatible service. This one
      field is why the nested position needs no separate read here: botocore
      has already put it where the top-level one goes. There is no
      ``Error.__type`` and no top-level ``__type`` to read: botocore never sets
      either, so looking for them would be reading a key only a test fixture
      could put there. ``Error.code`` is read because the Errors table names
      that spelling, but botocore does not use it;
    * the ``x-amzn-query-error`` header itself, which is on the response
      whether or not the parser preferred it.

    Each is read in every spelling ``_spellings`` lists. The shared fixtures
    under ``compat/model/testdata/errors`` pin the outcomes;
    ``tests/test_error_fixtures.py`` runs them against this."""
    names = [type(exc).__name__]
    response = getattr(exc, "response", None)
    if isinstance(response, dict):
        error = response.get("Error") or {}
        for key in ("Code", "code"):
            names.extend(_spellings(error.get(key)))
        headers = (response.get("ResponseMetadata") or {}).get("HTTPHeaders") or {}
        names.extend(_spellings(headers.get("x-amzn-query-error")))
    seen: list[str] = []
    for name in names:
        if name and name not in seen:
            seen.append(name)
    return seen


def _spellings(value: Any) -> list[str]:
    """One raw surface, in every spelling a clause may name it by: the value
    itself, what follows the last ``#`` of a Smithy id, and what precedes the
    first ``;`` of the header's ``<code>;<fault>`` form. Splitting only at
    those separators is what keeps matching an equality: no spelling of
    ``ResourceNotFoundException`` is ever ``NotFoundException``."""
    if not isinstance(value, str) or not value:
        return []
    out = [value]
    if "#" in value:
        out.append(value.rsplit("#", 1)[-1])
    if ";" in value:
        out.append(value.split(";", 1)[0])
    return out


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
