"""
lib/scenario — the python-sdk scenario interpreter.

`cmd/compatgen` generates a scenario IR (`compat/model/scenarios/<service>.json`,
specified normatively by `compat/model/README.md`) and a registry sibling that
points each generated group at its scenario file. This package executes that IR
with boto3, so a generated group needs no Python source of its own:

    loader.py       reads the scenario files the registry names
    expressions.py  the value expressions ($lit/$ref/$name/$concat/$index),
                    the path syntax, JSON equality and non-emptiness
    executor.py     the boto3 calls, the context bag, exports, error names
    assertions.py   the closed assertion set
    failures.py     the one six-field failure-message builder
    __init__.py     the loader hooks: a scenario backend plus the group's
                    setup and teardown

`runner.py` calls :func:`scenario_hooks` once, after loading the registry, and
hands the three results to `build_groups_from_registry`. Nothing else in the
suite knows the interpreter exists.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Optional

from ..harness import TestContext, TestFn
from ..registry import ScenarioBackend
from .assertions import Primary, check_clause
from .executor import ClientCache, Executor, StepRef
from .loader import ScenarioGroup, ScenarioLibrary

__all__ = ["ScenarioHooks", "ScenarioInterpreter", "scenario_hooks"]


@dataclass
class ScenarioHooks:
    """What `runner.py` needs to wire the interpreter into the loader."""

    backend: ScenarioBackend
    setup: dict[str, TestFn] = field(default_factory=dict)
    teardown: dict[str, TestFn] = field(default_factory=dict)


class ScenarioInterpreter:
    """Turns scenario groups into the callables the harness runs."""

    def __init__(self, library: Optional[ScenarioLibrary] = None,
                 clients: Optional[ClientCache] = None) -> None:
        self._library = library if library is not None else ScenarioLibrary()
        self._clients = clients if clients is not None else ClientCache()

    def group_spec(self, scenario: Optional[str], group: str) -> Optional[ScenarioGroup]:
        """The scenario group behind a registry group, or None."""
        return self._library.group(scenario, group)

    # ── the loader hook ─────────────────────────────────────────────────────

    def backend(self, group: str, test: str, scenario: Optional[str]) -> Optional[TestFn]:
        """`ScenarioBackend`: a TestFn for this (group, test), or None for
        anything this interpreter cannot resolve — a group with no `scenario`
        field, a scenario file that will not load, a group or test the file
        does not carry."""
        spec = self.group_spec(scenario, group)
        if spec is None or test not in spec.tests:
            return None
        return self.test_fn(spec, test)

    # ── the three callables ─────────────────────────────────────────────────

    def test_fn(self, spec: ScenarioGroup, test_name: str) -> TestFn:
        def run(ctx: TestContext) -> None:
            self.run_test(spec, test_name, ctx)
        run.__name__ = f"{spec.name}:{test_name}"
        return run

    def setup_fn(self, spec: ScenarioGroup) -> TestFn:
        def run(ctx: TestContext) -> None:
            self.run_setup(spec, ctx)
        run.__name__ = f"setup {spec.name}"
        return run

    def teardown_fn(self, spec: ScenarioGroup) -> TestFn:
        def run(ctx: TestContext) -> None:
            self.run_teardown(spec, ctx)
        run.__name__ = f"teardown {spec.name}"
        return run

    # ── execution ───────────────────────────────────────────────────────────

    def run_setup(self, spec: ScenarioGroup, ctx: TestContext) -> None:
        """Run every setup call in order. Raising is the contract: the harness
        turns a setup failure into `skip` with `setup failed: <message>` for
        every test of the group, and still runs teardown."""
        ex = Executor(spec, ctx, self._clients)
        for index, call in enumerate(spec.setup):
            ref = StepRef(group=spec.name, test="setup", file=spec.file,
                          step=f"setup[{index}]")
            ex.perform(call, ref, "call")

    def run_teardown(self, spec: ScenarioGroup, ctx: TestContext) -> None:
        """Run every teardown call in order, each wrapped individually: an
        error or an unresolvable `$ref` skips that call and continues with the
        next. Teardown never raises."""
        ex = Executor(spec, ctx, self._clients)
        for index, call in enumerate(spec.teardown):
            ref = StepRef(group=spec.name, test="teardown", file=spec.file,
                          step=f"teardown[{index}]")
            try:
                ex.perform(call, ref, "call")
            except Exception as exc:  # noqa: BLE001 - one step must not stop the rest
                ctx.log(f"teardown {spec.name} {ref.step} skipped: {exc}")

    def run_test(self, spec: ScenarioGroup, test_name: str, ctx: TestContext) -> None:
        test = spec.tests[test_name]
        ex = Executor(spec, ctx, self._clients)
        call = test["call"]
        ref = StepRef(group=spec.name, test=test_name, file=spec.file, step="call")

        # A test carrying an `errorCode` clause expects its own call to fail;
        # the generator refuses such a test any clause that reads the primary
        # response, so nothing downstream needs one.
        expects_error = any(c["kind"] == "errorCode" for c in test["assert"])
        if expects_error:
            params, response, error = ex.attempt(call, ref, "call")
        else:
            params, response = ex.perform(call, ref, "call")
            error = None

        primary = Primary(op=call["op"], params=params, response=response, error=error)
        for index, clause in enumerate(test["assert"]):
            check_clause(ex, clause, StepRef(group=spec.name, test=test_name,
                                             file=spec.file, step=f"assert[{index}]"),
                         primary)


def scenario_hooks(registry: dict, interpreter: Optional[ScenarioInterpreter] = None,
                   ) -> ScenarioHooks:
    """Build the backend and the setup/teardown maps for every generated group
    in `registry` that names a scenario file.

    The loader's hook resolves one test at a time and has nowhere to put a
    group's setup and teardown, so those are registered the way every other
    group's are — by group name, in the maps `build_groups_from_registry`
    already takes. Both are registered for every scenario group, even one whose
    lists are empty (a probe group has neither), so that setup and teardown are
    never present as a pair with one half missing."""
    interp = interpreter if interpreter is not None else ScenarioInterpreter()
    setup: dict[str, TestFn] = {}
    teardown: dict[str, TestFn] = {}

    for group in registry.get("groups", []):
        scenario = group.get("scenario")
        if not scenario:
            continue
        spec = interp.group_spec(scenario, group["name"])
        if spec is None:
            continue
        setup[spec.name] = interp.setup_fn(spec)
        teardown[spec.name] = interp.teardown_fn(spec)

    return ScenarioHooks(backend=interp.backend, setup=setup, teardown=teardown)
