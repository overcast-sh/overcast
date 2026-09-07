"""
lib/harness.py — Core test framework for the Overcast compat Python suite.

Mirrors the Node.js harness: emits NDJSON events to stdout, runs groups
sequentially, emits "skip" / "unimplemented" / "pass" / "fail" per test.

Rules:
- Never write non-NDJSON to stdout — use ctx.log() for debug output.
- Tests raise to signal failure; returning normally means pass.
- Teardown always runs, even if tests failed.
"""

from __future__ import annotations

import json
import os
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed, TimeoutError as FutureTimeoutError
from dataclasses import dataclass, field
from typing import Any, Callable, Awaitable, Optional, Union


# ─── Context ─────────────────────────────────────────────────────────────────

class TestContext:
    """
    Per-run context passed to every test function.

    Attributes are read-only (endpoint, region, run_id).
    Tests may add arbitrary keys for cross-test state within a group.
    """

    def __init__(self, endpoint: str, region: str, run_id: str) -> None:
        self.endpoint = endpoint
        self.region = region
        self.run_id = run_id
        self._state: dict[str, Any] = {}

    # Allow tests to store per-group state via ctx["key"] = value
    def __getitem__(self, key: str) -> Any:
        return self._state[key]

    def __setitem__(self, key: str, value: Any) -> None:
        self._state[key] = value

    def __contains__(self, key: str) -> bool:
        return key in self._state

    def get(self, key: str, default: Any = None) -> Any:
        return self._state.get(key, default)

    def log(self, msg: str) -> None:
        """Write a debug message to stderr (never stdout)."""
        sys.stderr.write(f"[compat:python-sdk] {msg}\n")


# ─── Types ────────────────────────────────────────────────────────────────────

TestFn = Callable[["TestContext"], None]

@dataclass
class TestCase:
    name: str
    fn: TestFn
    skip: Union[bool, str, None] = None
    op: Union[str, bool, None] = None   # False = suppress doc link
    na: Optional[str] = None  # N/A reason: SDK doesn't expose this operation
    # Tests in the SAME group that must have passed before this one runs.
    # run_group skips the test when any of them failed or was skipped.
    depends: list[str] = field(default_factory=list)


@dataclass
class TestGroup:
    suite: str
    service: str
    name: str
    tests: list[TestCase]
    setup: Optional[TestFn] = None
    teardown: Optional[TestFn] = None
    # Lets the group's tests run concurrently with one another, bounded by the
    # same slot count that bounds concurrent groups. Only a generated probe
    # group sets it (``parallel`` in registry.generated.json): its tests have
    # no setup, no teardown, no exports and no ``depends``, so nothing orders
    # them and no test can observe another's outcome. Results are still
    # emitted in declaration order, so the only observable difference is the
    # wall clock.
    parallel: bool = False


# ─── NDJSON emitters ─────────────────────────────────────────────────────────

# _emit_lock serialises writes to stdout so that threads running groups in
# parallel never produce interleaved NDJSON lines.
_emit_lock = threading.Lock()

def _emit(event: dict) -> None:
    line = json.dumps(event) + "\n"
    with _emit_lock:
        sys.stdout.write(line)
        sys.stdout.flush()


def emit_run_start(suite: str, endpoint: str, total_tests: int = 0) -> None:
    _emit({
        "event": "run_start",
        "suite": suite,
        "started_at": _iso_now(),
        "endpoint": endpoint,
        "version": "1",
        **(({"total_tests": total_tests}) if total_tests else {}),
    })


def emit_run_end(suite: str, passed: int, failed: int, skipped: int,
                 unimplemented: int, duration_ms: int) -> None:
    _emit({
        "event": "run_end",
        "suite": suite,
        "passed": passed,
        "failed": failed,
        "skipped": skipped,
        "unimplemented": unimplemented,
        "duration_ms": duration_ms,
    })


def emit_building(suite: str, message: str) -> None:
    _emit({"event": "building", "suite": suite, "message": message})


def emit_ready(suite: str, total_tests: int) -> None:
    _emit({"event": "ready", "suite": suite, "total_tests": total_tests})


def emit_batch_complete(suite: str, batch_id: str, passed: int, failed: int,
                        skipped: int, unimplemented: int, cancelled: int,
                        duration_ms: int) -> None:
    _emit({
        "event": "batch_complete",
        "suite": suite,
        "batch_id": batch_id,
        "passed": passed,
        "failed": failed,
        "skipped": skipped,
        "unimplemented": unimplemented,
        "cancelled": cancelled,
        "duration_ms": duration_ms,
    })


def cancelled_event(suite: str, batch_id: str, group: str, test: str,
                    reason: str = "") -> dict:
    """The `cancelled` event, built rather than emitted so a group collecting
    its results can hold one alongside the rest."""
    ev: dict = {"event": "cancelled", "suite": suite, "batch_id": batch_id,
                "group": group, "test": test}
    if reason:
        ev["reason"] = reason
    return ev


def emit_cancelled(suite: str, batch_id: str, group: str, test: str,
                   reason: str = "") -> None:
    _emit(cancelled_event(suite, batch_id, group, test, reason))


def _iso_now() -> str:
    from datetime import datetime, timezone
    return datetime.now(timezone.utc).isoformat()


# ─── Unimplemented detection ─────────────────────────────────────────────────

# The two error codes Overcast answers with for an operation it does not serve.
# UnknownOperationException comes back at HTTP 400, for a target naming no
# modeled operation, so the status alone would miss it.
_UNIMPLEMENTED_CODES = ("NotImplemented", "UnknownOperationException")


def _is_unimplemented(exc: Exception) -> bool:
    """Return True if the exception represents a 501 Not Implemented response.

    Decided from the **response botocore parsed**, never from the exception's
    prose. A ``ClientError`` carries the status, the headers and the error code,
    and once it has been read the answer is settled: a 400 stays a failure
    whatever its message, request id or ARN happens to contain.

    That mattered on #1924. The rule here used to end in a substring test, and
    the sibling go-sdk suite's version of it reported
    ``secretsmanager-rotate/RotateSecretWithoutLambda`` — a test that asserts an
    ``InvalidRequestException`` — as ``unimplemented`` on one CI run whose
    request id happened to contain "501", flipping a gated baseline row and
    failing an unrelated pull request.

    The substring fallback survives only for an exception carrying no response
    at all — a connection failure, an endpoint that would not resolve — where
    there is nothing else to read.
    """
    from botocore.exceptions import ClientError
    if isinstance(exc, ClientError):
        response = exc.response or {}
        metadata = response.get("ResponseMetadata", {}) or {}
        if metadata.get("HTTPStatusCode") == 501:
            return True
        headers = metadata.get("HTTPHeaders", {}) or {}
        # Overcast sets this alongside every 501 (internal/protocol/errors.go);
        # it survives a body botocore could not parse into a status.
        if str(headers.get("x-emulator-unsupported", "")).lower() == "true":
            return True
        return (response.get("Error", {}) or {}).get("Code", "") in _UNIMPLEMENTED_CODES
    return _looks_unimplemented_without_response(str(exc))


def _looks_unimplemented_without_response(msg: str) -> bool:
    """The substring heuristic, for an exception carrying no HTTP response.

    Never right for one that reached the wire: the response states the status,
    and "501" appears in request ids, ARNs, resource names and port numbers.
    """
    return "501" in msg and "Not Implemented" in msg


# ─── Parallel slots ───────────────────────────────────────────────────────────

def _parallel_slots() -> int:
    """How many things this suite may do at once: groups in ``run_suite``, and
    the tests of one parallel group in ``run_group``.

    ``OVERCAST_COMPAT_PARALLEL_SLOTS`` is injected by the Go runner from the CPU
    count and the number of active suites; default 8. One number bounds both
    because it answers one question — how much load this machine should put on
    the emulator at once — and a second knob would only let the two drift apart.
    """
    try:
        return max(1, int(os.environ.get("OVERCAST_COMPAT_PARALLEL_SLOTS", "8") or "8"))
    except ValueError:
        return 8


# ─── Group runner ─────────────────────────────────────────────────────────────

def run_group(group: TestGroup, ctx: TestContext, *,
              cancel_event: Optional[threading.Event] = None,
              batch_id: str = "") -> tuple[int, int, int, int, int]:
    """
    Run one test group synchronously.
    Returns (passed, failed, skipped, unimplemented, cancelled).
    """
    passed = failed = skipped = unimplemented = cancelled_count = 0

    # Tests that did not pass, so a test declaring one of them as a dependency
    # is skipped rather than run against a prerequisite that never happened.
    # "na" and cancelled are deliberately absent: neither says the resource a
    # dependent needs is missing.
    failed_or_skipped: set[str] = set()

    # Setup phase
    if group.setup:
        try:
            group.setup(ctx)
        except Exception as exc:
            reason = f"setup failed: {exc}"
            for tc in group.tests:
                _emit({
                    "event": "test_result",
                    "suite": group.suite,
                    "service": group.service,
                    "group": group.name,
                    "test": tc.name,
                    "status": "skip",
                    "duration_ms": 0,
                    "error": reason,
                })
                skipped += 1
            _run_teardown(group, ctx)
            return passed, failed, skipped, unimplemented, cancelled_count

    # A group marked parallel whose tests declare no dependencies runs them
    # concurrently; everything else runs in declaration order. Both halves of
    # that condition are load-bearing: the concurrent path cannot express the
    # dependency gate, which decides what to skip from outcomes that have not
    # happened yet, so a group declaring one is run serially even where the
    # registry says parallel. The IR never produces that combination — only a
    # probe group is parallel, and a probe has no exports for a ``depends`` to
    # consume — which is why this is a guard and not a scheduler.
    if group.parallel and not any(tc.depends for tc in group.tests):
        counts = _run_tests_concurrently(group, ctx, cancel_event=cancel_event,
                                         batch_id=batch_id)
    else:
        counts = _run_tests_in_order(group, ctx, failed_or_skipped,
                                     cancel_event=cancel_event, batch_id=batch_id)
    passed, failed, skipped, unimplemented, cancelled_count = counts

    _run_teardown(group, ctx)
    return passed, failed, skipped, unimplemented, cancelled_count


def _start_event(group: TestGroup, tc: TestCase) -> dict:
    return {"event": "test_start", "suite": group.suite,
            "service": group.service, "group": group.name, "test": tc.name}


def _result_event(group: TestGroup, tc: TestCase, status: str,
                  duration_ms: int, error: Optional[str] = None,
                  with_op: bool = False) -> dict:
    """One ``test_result`` event.

    ``with_op`` says whether the test's ``op`` is carried, and it is set
    exactly where it has always been set: on a result the test produced by
    running, never on an na/skip marker it never got to. ``op is False``
    suppresses the dashboard's doc link and is never emitted either way.
    """
    ev: dict[str, Any] = {
        "event": "test_result",
        "suite": group.suite,
        "service": group.service,
        "group": group.name,
        "test": tc.name,
        "status": status,
        "duration_ms": duration_ms,
    }
    if error is not None:
        ev["error"] = error
    if with_op and tc.op is not None and tc.op is not False:
        ev["op"] = tc.op
    return ev


def _marker(group: TestGroup, tc: TestCase) -> Optional[list[dict]]:
    """The events a test carries instead of running — na or skip — or None."""
    if tc.na:
        return [_start_event(group, tc),
                _result_event(group, tc, "na", 0, error=tc.na)]
    if tc.skip:
        reason = tc.skip if isinstance(tc.skip, str) else "skipped"
        return [_start_event(group, tc),
                _result_event(group, tc, "skip", 0, error=reason)]
    return None


def _run_one(group: TestGroup, ctx: TestContext, tc: TestCase) -> list[dict]:
    """Run one test, or report its na/skip marker without running it, and
    return the events it produces.

    Nothing here is shared with another test of the group beyond the
    TestContext, which a probe group never writes to, so it is safe to call
    concurrently for the tests of one parallel group — which is why the
    concurrent path calls this and the serial one calls ``_marker`` and
    ``_execute`` separately, with the dependency gate between them.
    """
    return _marker(group, tc) or _execute(group, ctx, tc)


def _execute(group: TestGroup, ctx: TestContext, tc: TestCase) -> list[dict]:
    """Run the test function and classify its outcome."""
    events = [_start_event(group, tc)]
    start = time.monotonic()
    try:
        tc.fn(ctx)
    except Exception as exc:
        duration = int((time.monotonic() - start) * 1000)
        status = "unimplemented" if _is_unimplemented(exc) else "fail"
        events.append(_result_event(group, tc, status, duration, error=str(exc), with_op=True))
        return events
    duration = int((time.monotonic() - start) * 1000)
    events.append(_result_event(group, tc, "pass", duration, with_op=True))
    return events


# Counts, in the order run_group returns them.
Counts = tuple[int, int, int, int, int]


def _tally(events: list[dict]) -> Counts:
    """Fold a test's events into the group counters. "na" is counted nowhere:
    it is excluded from pass-rate calculations."""
    passed = failed = skipped = unimplemented = cancelled = 0
    for ev in events:
        if ev["event"] == "cancelled":
            cancelled += 1
            continue
        status = ev.get("status")
        if status == "pass":
            passed += 1
        elif status == "skip":
            skipped += 1
        elif status == "unimplemented":
            unimplemented += 1
        elif status == "fail":
            failed += 1
    return passed, failed, skipped, unimplemented, cancelled


def _add(a: Counts, b: Counts) -> Counts:
    return tuple(x + y for x, y in zip(a, b))  # type: ignore[return-value]


def _run_tests_in_order(group: TestGroup, ctx: TestContext,
                        failed_or_skipped: set[str], *,
                        cancel_event: Optional[threading.Event],
                        batch_id: str) -> Counts:
    """The serial path: one test at a time, in declaration order, each event
    emitted as it happens so the dashboard sees the group progress."""
    counts: Counts = (0, 0, 0, 0, 0)
    for tc in group.tests:
        if cancel_event and cancel_event.is_set():
            events = [cancelled_event(group.suite, batch_id, group.name,
                                      tc.name, "user")]
        else:
            # An na/skip marker outranks the dependency gate: a test the suite
            # never intended to run here reports why it was marked, not what
            # happened to something it does not depend on.
            events = _marker(group, tc)
            if events is None:
                # Dependency gate — skip if any declared dependency failed or
                # was skipped. Without it a single broken prerequisite reports
                # as a cascade of unrelated failures, and "dependency failed: X"
                # is what tells a reader the cause is elsewhere in the group.
                failed_deps = [d for d in tc.depends if d in failed_or_skipped]
                if failed_deps:
                    events = [
                        _start_event(group, tc),
                        _result_event(group, tc, "skip", 0,
                                      error=f"dependency failed: {', '.join(failed_deps)}"),
                    ]
                else:
                    events = _execute(group, ctx, tc)
            if events[-1].get("status") in ("skip", "fail", "unimplemented"):
                failed_or_skipped.add(tc.name)
        for ev in events:
            _emit(ev)
        counts = _add(counts, _tally(events))
    return counts


def _run_tests_concurrently(group: TestGroup, ctx: TestContext, *,
                            cancel_event: Optional[threading.Event],
                            batch_id: str) -> Counts:
    """Run the group's tests through a bounded thread pool, then emit their
    events in declaration order.

    Emitting in order rather than as each finishes is what keeps this stream
    identical to the serial path's, test for test. The dashboard, the baseline
    and the flake detector all read it, and a result order that depended on
    which boto3 call answered first would be a new source of diff noise for no
    benefit.

    No dependency bookkeeping: this path is taken only when no test declares
    one, so the set a serial run maintains would be read by nobody.
    """
    per_test: list[list[dict]] = [[] for _ in group.tests]
    with ThreadPoolExecutor(max_workers=_parallel_slots()) as pool:
        futures = {}
        for i, tc in enumerate(group.tests):
            if cancel_event and cancel_event.is_set():
                per_test[i] = [cancelled_event(group.suite, batch_id,
                                               group.name, tc.name, "user")]
                continue
            futures[pool.submit(_run_one, group, ctx, tc)] = i
        for future, i in futures.items():
            per_test[i] = future.result()

    counts: Counts = (0, 0, 0, 0, 0)
    for events in per_test:
        for ev in events:
            _emit(ev)
        counts = _add(counts, _tally(events))
    return counts


def _run_teardown(group: TestGroup, ctx: TestContext) -> None:
    if group.teardown:
        try:
            group.teardown(ctx)
        except Exception as exc:
            sys.stderr.write(f"[compat:python-sdk] teardown {group.name} failed: {exc}\n")


# ─── Suite runner ─────────────────────────────────────────────────────────────

def run_suite(suite: str, groups: list[TestGroup], endpoint: str,
              region: str, run_id: str) -> None:
    """Run all groups in parallel, emit NDJSON events, finalize with run_end.

    Each group receives its own fresh TestContext so that per-group state
    stored via ctx["key"] = value does not leak between concurrent groups.
    """
    total_tests = sum(len(g.tests) for g in groups)
    emit_run_start(suite, endpoint, total_tests=total_tests)
    start = time.monotonic()

    def _run_one(group: TestGroup) -> tuple[int, int, int, int, int]:
        ctx = TestContext(endpoint=endpoint, region=region, run_id=run_id)
        return run_group(group, ctx)

    # Limit concurrent group execution to avoid overwhelming the emulator.
    max_workers = _parallel_slots()

    total_passed = total_failed = total_skipped = total_unimplemented = 0
    with ThreadPoolExecutor(max_workers=max_workers) as executor:
        futures = {executor.submit(_run_one, g): g for g in groups}
        # as_completed with a total budget prevents one stuck group from
        # blocking the suite forever.  25 minutes covers the worst-case
        # scenario of many slow groups; individual groups that hang will be
        # cancelled by the runner.go suite-level timeout (25 min).
        SUITE_TIMEOUT_S = 25 * 60
        try:
            for future in as_completed(futures, timeout=SUITE_TIMEOUT_S):
                group = futures[future]
                try:
                    p, f, s, u, _c = future.result()
                    total_passed += p
                    total_failed += f
                    total_skipped += s
                    total_unimplemented += u
                except Exception as exc:
                    sys.stderr.write(
                        f"[compat:python-sdk] group {group.name} raised: {exc}\n"
                    )
        except FutureTimeoutError:
            sys.stderr.write(
                "[compat:python-sdk] suite timed out — some groups did not finish\n"
            )

    duration_ms = int((time.monotonic() - start) * 1000)
    emit_run_end(suite, total_passed, total_failed, total_skipped,
                 total_unimplemented, duration_ms)


def make_run_id() -> str:
    import secrets
    return "oc-" + secrets.token_hex(4)


# ─── Stdin command reader (interactive mode) ─────────────────────────────────

def read_commands():
    """Generator that yields parsed dicts from stdin NDJSON."""
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            cmd = json.loads(line)
            yield cmd
        except json.JSONDecodeError:
            sys.stderr.write(f"[harness] invalid JSON on stdin: {line}\n")
