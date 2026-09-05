#!/usr/bin/env python3
"""
runner.py — Entry point for the python-sdk compatibility suite.

Usage:
    python3 runner.py

Environment variables:
    OVERCAST_ENDPOINT            Emulator endpoint (default: http://localhost:4566)
    OVERCAST_DEFAULT_REGION              AWS region (default: us-east-1)
    OVERCAST_COMPAT_RUN_ID       Deterministic run ID for reproducible naming
    OVERCAST_COMPAT_SKIP_DOCKER  Set to "1" to skip tests that require Docker
    OVERCAST_COMPAT_SERVICE      Comma-separated service names to run (e.g. "s3,sqs")
    OVERCAST_COMPAT_GROUPS       Comma-separated group names to run
    OVERCAST_COMPAT_TESTS        Comma-separated test names to run
    OVERCAST_COMPAT_STRICT       Set to "1" to treat orphan impls as a fatal error
"""

from __future__ import annotations

import os
import signal
import sys
import time
import threading

# Ensure the suite directory is on the path
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from lib.harness import TestGroup, TestContext, run_suite, run_group, make_run_id
from lib.harness import (emit_building, emit_ready, emit_batch_complete,
                          emit_cancelled, read_commands)
from lib.registry import (load_registry, build_groups_from_registry, merge_impls,
                          validate_impls)
from lib.scenario import scenario_hooks
from groups import (
    s3,
    sqs,
    dynamodb,
    sns,
    lambda_,
    cloudwatch_logs,
    ses,
    iam,
    sts,
    secretsmanager,
    kms,
    ssm,
    kinesis,
    eventbridge,
    cloudformation,
    ec2,
    ecs,
    cognito,
    appsync,
    apigateway,
    cloudfront,
    elasticache,
    rds,
    stepfunctions,
    pipes,
    waf,
    shield,
    efs,
)

SUITE = "python-sdk"

# ─── Configuration ─────────────────────────────────────────────────────────────

endpoint = os.environ.get("OVERCAST_ENDPOINT", "http://localhost:4566")
region = os.environ.get("OVERCAST_DEFAULT_REGION", "us-east-1")
run_id = os.environ.get("OVERCAST_COMPAT_RUN_ID") or make_run_id()
skip_docker = os.environ.get("OVERCAST_COMPAT_SKIP_DOCKER") == "1"

# Optional filters
_filter_services = {s.strip() for s in os.environ.get("OVERCAST_COMPAT_SERVICE", "").split(",") if s.strip()}
_filter_groups = {g.strip() for g in os.environ.get("OVERCAST_COMPAT_GROUPS", "").split(",") if g.strip()}
_filter_tests = {t.strip() for t in os.environ.get("OVERCAST_COMPAT_TESTS", "").split(",") if t.strip()}

# ─── Assemble impls ────────────────────────────────────────────────────────────

_modules = [
    s3,
    sqs,
    dynamodb,
    sns,
    lambda_,
    cloudwatch_logs,
    ses,
    iam,
    sts,
    secretsmanager,
    kms,
    ssm,
    kinesis,
    eventbridge,
    cloudformation,
    ec2,
    ecs,
    cognito,
    appsync,
    apigateway,
    cloudfront,
    elasticache,
    rds,
    stepfunctions,
    pipes,
    waf,
    shield,
    efs,
]

all_setup: dict = {}
all_teardown: dict = {}

for mod in _modules:
    all_setup.update(mod.SETUP)
    all_teardown.update(mod.TEARDOWN)

# The impls go through merge_impls rather than dict.update: a key two group
# modules both register would otherwise lose one implementation with nothing
# said about it. Labelled with the module name so a collision names both files.
all_impls = merge_impls(((mod.__name__, mod.IMPLS) for mod in _modules), SUITE)

# ─── Build groups from registry ────────────────────────────────────────────────

registry = load_registry()
validate_impls(registry, all_impls, SUITE)

# The scenario interpreter (#1113 phase G2) supplies the implementations for
# generated groups — the ones registry.generated.json points at a scenario file
# — plus their setup and teardown, which the loader's per-test hook has nowhere
# to carry. Hand-written groups are untouched: the backend is consulted only
# for a test with no registered impl, and it answers None for anything outside
# a scenario file.
scenario = scenario_hooks(registry)
all_setup.update(scenario.setup)
all_teardown.update(scenario.teardown)

groups = build_groups_from_registry(
    registry,
    all_impls,
    SUITE,
    capabilities=set() if skip_docker else {"docker"},
    setup=all_setup,
    teardown=all_teardown,
    scenario_backend=scenario.backend,
)

# ─── Apply filters ─────────────────────────────────────────────────────────────

if _filter_services:
    groups = [g for g in groups if g.service in _filter_services]

if _filter_groups:
    groups = [g for g in groups if g.name in _filter_groups]

if _filter_tests:
    filtered: list[TestGroup] = []
    for g in groups:
        tests = [t for t in g.tests if t.name in _filter_tests]
        if tests:
            from dataclasses import replace
            filtered.append(replace(g, tests=tests))
    groups = filtered

# ─── Run ───────────────────────────────────────────────────────────────────────

is_interactive = os.environ.get("OVERCAST_COMPAT_INTERACTIVE") == "1"

# Global cancel event — set on SIGINT/SIGTERM or on "cancel" command.
_cancel_event = threading.Event()
_shutting_down = False

def _on_signal(sig, frame):
    global _shutting_down
    if _shutting_down:
        return
    _shutting_down = True
    sys.stderr.write(f"[python-sdk] received signal {sig} — shutting down\n")
    _cancel_event.set()
    sys.exit(0)

signal.signal(signal.SIGINT, _on_signal)
signal.signal(signal.SIGTERM, _on_signal)

if is_interactive:
    from dataclasses import replace as dc_replace
    from concurrent.futures import ThreadPoolExecutor, as_completed

    emit_building(SUITE, "Loading registry and building test groups...")

    total_tests = sum(len(g.tests) for g in groups)
    emit_ready(SUITE, total_tests)

    # Build group lookup
    group_map = {g.name: g for g in groups}

    # Shared cancel event — set on "cancel" command or SIGINT/SIGTERM
    cancel_event = _cancel_event

    # Track the currently executing test for ping/pong responses.
    # Protected by a lock since the command loop and test threads share it.
    running_test_lock = threading.Lock()
    running_test = [""]  # mutable container to allow assignment from nested functions

    def _run_batch(batch_id: str, groups_to_run: list[TestGroup]) -> None:
        """Execute a batch in a background thread so the command loop stays hot."""
        cancel_event.clear()
        batch_start = time.monotonic()

        max_workers = max(1, int(os.environ.get("OVERCAST_COMPAT_PARALLEL_SLOTS", "8") or "8"))

        def _run_one(g: TestGroup) -> tuple[int, int, int, int, int]:
            ctx_inner = TestContext(endpoint=endpoint, region=region, run_id=run_id)
            with running_test_lock:
                # Set running_test to the first non-skipped test in this group
                for t in g.tests:
                    if not t.skip:
                        running_test[0] = f"{g.name}:{t.name}"
                        break
            result = run_group(g, ctx_inner, cancel_event=cancel_event, batch_id=batch_id)
            return result

        total_p = total_f = total_s = total_u = total_c = 0

        with ThreadPoolExecutor(max_workers=max_workers) as executor:
            futures = {executor.submit(_run_one, g): g for g in groups_to_run}
            for future in as_completed(futures):
                try:
                    p, f, s, u, c = future.result()
                    total_p += p; total_f += f; total_s += s
                    total_u += u; total_c += c
                except Exception as exc:
                    sys.stderr.write(f"[python-sdk] group error: {exc}\n")

        with running_test_lock:
            running_test[0] = ""

        duration_ms = int((time.monotonic() - batch_start) * 1000)
        # Emit batch_complete after all groups finish.  Guard against
        # emitting events after shutdown (stdout may be closed / broken).
        try:
            emit_batch_complete(SUITE, batch_id, total_p, total_f, total_s,
                                total_u, total_c, duration_ms)
        except BrokenPipeError:
            pass

    for cmd in read_commands():
        command = cmd.get("command")

        if command == "run":
            batch_id = cmd.get("batch_id", "")

            groups_to_run: list[TestGroup] = []
            # Empty or absent tests means "run all groups".
            if not cmd.get("tests"):
                groups_to_run.extend(groups)
            for ref in cmd.get("tests", []):
                g = group_map.get(ref["group"])
                if not g:
                    sys.stderr.write(f"[python-sdk] unknown group: {ref['group']}\n")
                    continue
                if ref.get("tests"):
                    requested = set(ref["tests"])
                    g = dc_replace(g, tests=[t for t in g.tests if t.name in requested])
                groups_to_run.append(g)

            # Launch test execution off the main thread so the command loop
            # can still process ping, cancel, and shutdown commands.
            threading.Thread(target=_run_batch, args=(batch_id, groups_to_run), daemon=True).start()

        elif command == "cancel":
            cancel_event.set()

        elif command == "ping":
            cur = ""
            with running_test_lock:
                cur = running_test[0]
            import json as _json
            sys.stdout.write(_json.dumps({
                "event": "pong",
                "suite": SUITE,
                "running_test": cur,
            }) + "\n")
            sys.stdout.flush()

        elif command == "shutdown":
            cancel_event.set()
            sys.exit(0)

    # stdin closed = implicit shutdown
    sys.exit(0)

else:
    run_suite(SUITE, groups, endpoint, region, run_id)
