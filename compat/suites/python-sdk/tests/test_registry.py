"""
Unit tests for the registry loader's impl-key resolution rules.

These are not compat tests — they need no emulator. They pin the two rules that
stop a run from reporting a result for a test that never executed:

- a key that resolves to nothing aborts, instead of warning;
- a bare key for a name several groups declare is refused, instead of binding
  whichever group's implementation happened to be registered last.

Run with:  python -m unittest discover -s tests  (from compat/suites/python-sdk/)
"""

from __future__ import annotations

import contextlib
import io
import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from lib.harness import TestContext, run_group  # noqa: E402
from lib.registry import (  # noqa: E402
    ambiguous_test_names,
    build_groups_from_registry,
    load_generated_registry,
    load_registry,
    merge_impls,
    merge_registries,
    test_name_owners,
    validate_impls,
)


def _noop(ctx):
    return None


# Two unrelated groups declaring a test of the same name, plus a name owned by
# exactly one group — the shape that made a mis-binding possible.
TWO_GROUPS_ONE_NAME = {
    "groups": [
        {
            "service": "iam",
            "name": "iam-users",
            "tests": [{"name": "ListUsers"}, {"name": "CreateUser"}],
        },
        {
            "service": "cognito",
            "name": "cognito-userpools",
            "tests": [{"name": "ListUsers"}],
        },
    ]
}


def _find(groups, group_name, test_name):
    for g in groups:
        if g.name != group_name:
            continue
        for tc in g.tests:
            if tc.name == test_name:
                return tc
    raise AssertionError(f"no test {group_name}/{test_name} in built groups")


class UnresolvableKeysAbort(unittest.TestCase):
    def test_wrong_separator(self):
        with self.assertRaises(SystemExit) as cm:
            validate_impls(TWO_GROUPS_ONE_NAME, {"iam-users/CreateUser": _noop}, "python-sdk")
        message = str(cm.exception)
        self.assertIn("iam-users/CreateUser", message)
        self.assertIn("matches no registry entry", message)
        # The message must point at the colon form, since that is the fix.
        self.assertIn("iam-users:CreateUser", message)

    def test_unknown_group(self):
        with self.assertRaises(SystemExit) as cm:
            validate_impls(TWO_GROUPS_ONE_NAME, {"iam-usres:CreateUser": _noop}, "python-sdk")
        self.assertIn("iam-usres:CreateUser", str(cm.exception))

    def test_unknown_test(self):
        with self.assertRaises(SystemExit) as cm:
            validate_impls(TWO_GROUPS_ONE_NAME, {"CreateUsr": _noop}, "python-sdk")
        self.assertIn("CreateUsr", str(cm.exception))


class AmbiguousBareKeysRefused(unittest.TestCase):
    def test_bare_key_for_shared_name(self):
        with self.assertRaises(SystemExit) as cm:
            validate_impls(TWO_GROUPS_ONE_NAME, {"ListUsers": _noop}, "python-sdk")
        message = str(cm.exception)
        self.assertIn("ambiguous", message)
        self.assertIn("iam-users", message)
        self.assertIn("cognito-userpools", message)

    def test_resolvable_keys_accepted(self):
        validate_impls(
            TWO_GROUPS_ONE_NAME,
            {
                "CreateUser": _noop,  # bare, single owner
                "iam-users:ListUsers": _noop,
                "cognito-userpools:ListUsers": _noop,
            },
            "python-sdk",
        )


class BuildGroupsResolution(unittest.TestCase):
    """Defence in depth: even bypassing validation, no cross-group binding."""

    def test_cross_group_bare_fallback_refused(self):
        groups = build_groups_from_registry(
            TWO_GROUPS_ONE_NAME, {"ListUsers": _noop}, "python-sdk"
        )
        cognito = _find(groups, "cognito-userpools", "ListUsers")
        self.assertEqual(cognito.skip, "not yet implemented in python-sdk test suite")
        self.assertTrue(_find(groups, "iam-users", "ListUsers").skip)

    def test_qualified_key_binds_only_its_group(self):
        groups = build_groups_from_registry(
            TWO_GROUPS_ONE_NAME, {"iam-users:ListUsers": _noop}, "python-sdk"
        )
        self.assertFalse(_find(groups, "iam-users", "ListUsers").skip)
        self.assertTrue(_find(groups, "cognito-userpools", "ListUsers").skip)

    def test_unambiguous_bare_fallback_still_works(self):
        groups = build_groups_from_registry(
            TWO_GROUPS_ONE_NAME, {"CreateUser": _noop}, "python-sdk"
        )
        self.assertFalse(_find(groups, "iam-users", "CreateUser").skip)


def _order(groups, group_name):
    """Names of a built group's tests, in run order."""
    for g in groups:
        if g.name == group_name:
            return [tc.name for tc in g.tests]
    raise AssertionError(f"group {group_name!r} not built")


def _all_impls(registry):
    """A passing impl for every registry test, so ordering is observed on real
    test cases rather than auto-skips."""
    return {
        f"{rg['name']}:{rt['name']}": _noop
        for rg in registry["groups"]
        for rt in rg["tests"]
    }


class DependencyOrdering(unittest.TestCase):
    """A group runs in `depends` order, not registry file order.

    `cloudformation-stacks` listed DeleteStack before the UpdateStack it depends
    on, so this suite deleted the shared stack and then updated it, while the cli
    and node-js suites — which already sort — did not. One registry, a different
    order per language.
    """

    def test_dependency_runs_before_its_dependent(self):
        registry = {
            "groups": [
                {
                    "service": "cloudformation",
                    "name": "stacks",
                    "tests": [
                        {"name": "CreateStack"},
                        {"name": "DeleteStack", "depends": ["UpdateStack"]},
                        {"name": "ValidateTemplate"},
                        {"name": "UpdateStack", "depends": ["CreateStack"]},
                    ],
                }
            ]
        }
        order = _order(
            build_groups_from_registry(registry, _all_impls(registry), "python-sdk"),
            "stacks",
        )
        self.assertLess(order.index("UpdateStack"), order.index("DeleteStack"), order)
        self.assertLess(order.index("CreateStack"), order.index("UpdateStack"), order)

    def test_declaration_order_kept_within_a_depth(self):
        # Sorting must reorder only what the edges require; a group that is free
        # to rearrange run to run makes a failure hard to reproduce.
        registry = {
            "groups": [
                {
                    "service": "s3",
                    "name": "s3-crud",
                    "tests": [
                        {"name": "CreateBucket"},
                        {"name": "PutObject", "depends": ["CreateBucket"]},
                        {"name": "GetObject", "depends": ["PutObject"]},
                        {"name": "ListObjects", "depends": ["PutObject"]},
                    ],
                }
            ]
        }
        order = _order(
            build_groups_from_registry(registry, _all_impls(registry), "python-sdk"),
            "s3-crud",
        )
        self.assertEqual(
            order, ["CreateBucket", "PutObject", "GetObject", "ListObjects"]
        )

    def test_cycle_neither_hangs_nor_drops_tests(self):
        # A cycle is a registry bug, but the sort must still emit every test once.
        registry = {
            "groups": [
                {
                    "service": "sqs",
                    "name": "cyclic",
                    "tests": [
                        {"name": "A", "depends": ["B"]},
                        {"name": "B", "depends": ["A"]},
                        {"name": "C"},
                    ],
                }
            ]
        }
        order = _order(
            build_groups_from_registry(registry, _all_impls(registry), "python-sdk"),
            "cyclic",
        )
        self.assertEqual(sorted(order), ["A", "B", "C"], order)

    def test_unknown_dependency_drops_nothing(self):
        # Only same-group edges order a run, per the registry schema; a stale
        # name must not silently remove the test or its dependent.
        registry = {
            "groups": [
                {
                    "service": "sqs",
                    "name": "queues",
                    "tests": [
                        {"name": "CreateQueue"},
                        {
                            "name": "SendMessage",
                            "depends": ["CreateQueue", "NotInThisGroup"],
                        },
                    ],
                }
            ]
        }
        order = _order(
            build_groups_from_registry(registry, _all_impls(registry), "python-sdk"),
            "queues",
        )
        self.assertEqual(order, ["CreateQueue", "SendMessage"])

    def test_real_registry_builds_in_dependency_order(self):
        # The registry the suites actually run must sort the same way here as it
        # does in the cli and node-js suites.
        registry = load_registry()
        groups = build_groups_from_registry(
            registry, _all_impls(registry), "python-sdk"
        )
        declared = {
            rg["name"]: {rt["name"]: rt.get("depends") or [] for rt in rg["tests"]}
            for rg in registry["groups"]
        }
        for group in groups:
            ran: set[str] = set()
            for tc in group.tests:
                for dep in declared[group.name].get(tc.name, []):
                    if dep in declared[group.name] and dep not in ran:
                        self.fail(
                            f"{group.name}: {tc.name} runs before its dependency {dep}"
                        )
                ran.add(tc.name)


def _boom(ctx):
    """An impl that fails, so a prerequisite can be broken without an emulator."""
    raise RuntimeError("boom")


def _run_one_group(registry, impls):
    """Build the registry's single group and run it, returning
    ((passed, failed, skipped, unimplemented, cancelled), {test name: result}).

    The harness writes NDJSON to stdout, so stdout is captured for the run.
    """
    groups = build_groups_from_registry(registry, impls, "python-sdk")
    assert len(groups) == 1, f"built {len(groups)} groups, want exactly 1"

    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        counts = run_group(groups[0], TestContext("", "us-east-1", "test"))

    results = {}
    for line in buf.getvalue().splitlines():
        if not line.strip():
            continue
        event = json.loads(line)
        if event.get("event") == "test_result":
            results[event["test"]] = event
    return counts, results


class DependencyCascadeSkip(unittest.TestCase):
    """A test whose dependency did not pass is skipped, not run.

    Sorting by ``depends`` puts a prerequisite first; this half decides what
    happens when that prerequisite does not pass. The schema promises runners
    "auto-skip dependents when a dependency fails", and compat/AGENTS.md tells
    readers that "dependency failed: X" means the cause is another failure in the
    same group — advice that only holds if the harness emits it. Without the
    gate, one root cause reports as a cascade of unrelated failures and every one
    of them has to be triaged.
    """

    def test_failed_dependency_skips_its_dependents(self):
        registry = {
            "groups": [
                {
                    "service": "s3",
                    "name": "s3-crud",
                    "tests": [
                        {"name": "CreateBucket"},
                        {"name": "PutObject", "depends": ["CreateBucket"]},
                        {"name": "GetObject", "depends": ["PutObject"]},
                        {"name": "ListBuckets"},
                    ],
                }
            ]
        }
        impls = _all_impls(registry)
        impls["s3-crud:CreateBucket"] = _boom

        counts, results = _run_one_group(registry, impls)

        self.assertEqual("fail", results["CreateBucket"]["status"])
        self.assertEqual("skip", results["PutObject"]["status"])
        self.assertEqual(
            "dependency failed: CreateBucket", results["PutObject"]["error"]
        )
        # The skip has to propagate: GetObject depends on PutObject, which never
        # ran. Blocking only the direct dependents would leave the second rank
        # failing for the same single cause.
        self.assertEqual("skip", results["GetObject"]["status"])
        self.assertEqual(
            "dependency failed: PutObject", results["GetObject"]["error"]
        )
        # A test with no edge to the failure is unaffected — the gate must not
        # quarantine the rest of the group.
        self.assertEqual("pass", results["ListBuckets"]["status"])

        passed, failed, skipped, _unimplemented, _cancelled = counts
        self.assertEqual((1, 1, 2), (passed, failed, skipped))

    def test_skipped_dependency_also_blocks(self):
        # A dependency that was skipped rather than failed is just as absent —
        # an unimplemented test creates nothing for its dependents to act on.
        registry = {
            "groups": [
                {
                    "service": "sqs",
                    "name": "queues",
                    "tests": [
                        {"name": "CreateQueue"},
                        {"name": "SendMessage", "depends": ["CreateQueue"]},
                    ],
                }
            ]
        }
        # No impl for CreateQueue → built as a "not yet implemented" skip.
        _counts, results = _run_one_group(registry, {"queues:SendMessage": _noop})

        self.assertEqual("skip", results["CreateQueue"]["status"])
        self.assertEqual("skip", results["SendMessage"]["status"])
        self.assertEqual(
            "dependency failed: CreateQueue", results["SendMessage"]["error"]
        )

    def test_every_blocking_dependency_is_named(self):
        # The reason line is the whole explanation a reader gets for the skip.
        registry = {
            "groups": [
                {
                    "service": "dynamodb",
                    "name": "ddb",
                    "tests": [
                        {"name": "CreateTable"},
                        {"name": "PutItem"},
                        {"name": "Query", "depends": ["CreateTable", "PutItem"]},
                    ],
                }
            ]
        }
        impls = _all_impls(registry)
        impls["ddb:CreateTable"] = _boom
        impls["ddb:PutItem"] = _boom

        _counts, results = _run_one_group(registry, impls)
        self.assertEqual(
            "dependency failed: CreateTable, PutItem", results["Query"]["error"]
        )

    def test_passing_dependencies_change_nothing(self):
        # The gate is inert on a green run — this is why a passing suite sees no
        # change from it, and why the baseline needs no update.
        registry = {
            "groups": [
                {
                    "service": "s3",
                    "name": "s3-crud",
                    "tests": [
                        {"name": "CreateBucket"},
                        {"name": "PutObject", "depends": ["CreateBucket"]},
                        {"name": "GetObject", "depends": ["PutObject"]},
                    ],
                }
            ]
        }
        counts, results = _run_one_group(registry, _all_impls(registry))

        for name, result in results.items():
            self.assertEqual("pass", result["status"], f"{name}: {result}")
        passed, failed, skipped, _u, _c = counts
        self.assertEqual((3, 0, 0), (passed, failed, skipped))

    def test_unknown_dependency_never_blocks(self):
        # `depends` is same-group only, per the registry schema. A dependency
        # the group does not declare cannot have failed, so a stale name left in
        # the registry must not silently skip a working test.
        registry = {
            "groups": [
                {
                    "service": "sqs",
                    "name": "queues",
                    "tests": [
                        {"name": "CreateQueue"},
                        {
                            "name": "SendMessage",
                            "depends": ["CreateQueue", "NotInThisGroup"],
                        },
                    ],
                }
            ]
        }
        _counts, results = _run_one_group(registry, _all_impls(registry))
        self.assertEqual("pass", results["SendMessage"]["status"])

    def test_na_dependency_does_not_block(self):
        # "na" means boto3 does not expose the operation, not that the run is
        # broken, and the cli, node-js, Go, Java, .NET and Rust suites all let
        # dependents through on it — a suite that blocked here would report a
        # different result for the same registry.
        registry = {
            "groups": [
                {
                    "service": "s3",
                    "name": "s3-crud",
                    "tests": [
                        {"name": "CreateBucket"},
                        {"name": "PutObject", "depends": ["CreateBucket"]},
                    ],
                }
            ]
        }
        impls = _all_impls(registry)
        # A None impl is the registry's "boto3 has no such call" signal → "na".
        impls["s3-crud:CreateBucket"] = None

        _counts, results = _run_one_group(registry, impls)
        self.assertEqual("na", results["CreateBucket"]["status"])
        self.assertEqual("pass", results["PutObject"]["status"])


class OwnerTracking(unittest.TestCase):
    def test_owners_and_ambiguity(self):
        self.assertIn("ListUsers", ambiguous_test_names(TWO_GROUPS_ONE_NAME))
        self.assertNotIn("CreateUser", ambiguous_test_names(TWO_GROUPS_ONE_NAME))
        self.assertEqual(
            ["cognito-userpools", "iam-users"],
            test_name_owners(TWO_GROUPS_ONE_NAME)["ListUsers"],
        )


class DuplicateRegistrationsAbort(unittest.TestCase):
    """
    Two group modules registering the same key must abort the run.

    This is the gap validate_impls cannot close. The merge that builds the
    suite's impl map is last-writer-wins, so one of the two implementations is
    discarded before validation ever sees the map — and the surviving key
    resolves perfectly well, so nothing is reported. The discarded test then
    runs the other module's implementation under its own name.
    """

    def test_qualified_key_registered_twice(self):
        with self.assertRaises(SystemExit) as cm:
            merge_impls(
                [
                    ("groups.lambda_", {"lambda-crud:CreateFunction": _noop}),
                    ("groups.appsync", {"lambda-crud:CreateFunction": _noop}),
                ],
                "python-sdk",
            )
        message = str(cm.exception)
        self.assertIn("duplicate impl registration", message)
        self.assertIn("lambda-crud:CreateFunction", message)
        # Both registering modules must be named: the key alone does not say
        # where to look, and one of the two modules is in the wrong.
        self.assertIn("groups.lambda_", message)
        self.assertIn("groups.appsync", message)

    def test_bare_key_registered_twice(self):
        with self.assertRaises(SystemExit) as cm:
            merge_impls(
                [("groups.iam", {"CreateUser": _noop}), ("groups.cognito", {"CreateUser": _noop})],
                "python-sdk",
            )
        message = str(cm.exception)
        self.assertIn("CreateUser", message)
        self.assertIn("groups.iam", message)
        self.assertIn("groups.cognito", message)

    def test_single_source_duplicate_reads_as_such(self):
        # "both X and Y" would be nonsense when X and Y are the same module.
        with self.assertRaises(SystemExit) as cm:
            merge_impls(
                [("groups.iam", {"CreateUser": _noop}), ("groups.iam", {"CreateUser": _noop})],
                "python-sdk",
            )
        self.assertIn("registered twice by 'groups.iam'", str(cm.exception))

    def test_every_duplicate_reported(self):
        # Fixing one duplicate must not merely reveal the next.
        with self.assertRaises(SystemExit) as cm:
            merge_impls(
                [
                    ("groups.iam", {"iam-users:ListUsers": _noop, "iam-users:CreateUser": _noop}),
                    ("groups.cognito", {"iam-users:ListUsers": _noop, "iam-users:CreateUser": _noop}),
                ],
                "python-sdk",
            )
        message = str(cm.exception)
        self.assertIn("2 duplicate impl registration(s)", message)
        # Sorted by key, so the message is stable run to run.
        self.assertLess(
            message.index("iam-users:CreateUser"), message.index("iam-users:ListUsers")
        )

    def test_disjoint_sources_merge(self):
        """Negative control: distinct keys merge, each keeping its own impl."""
        iam_list, iam_create, cognito_list = _noop, (lambda ctx: None), (lambda ctx: None)
        merged = merge_impls(
            [
                ("groups.iam", {"iam-users:ListUsers": iam_list, "CreateUser": iam_create}),
                ("groups.cognito", {"cognito-userpools:ListUsers": cognito_list}),
            ],
            "python-sdk",
        )
        self.assertEqual(
            {"iam-users:ListUsers": iam_list,
             "CreateUser": iam_create,
             "cognito-userpools:ListUsers": cognito_list},
            merged,
        )


class RealRegistrations(unittest.TestCase):
    """
    The suite's own registrations must resolve against the real registry.json.

    This is the check that catches a mis-binding before a run reports one, in
    `python -m unittest` rather than in results that silently describe the wrong
    test.

    The group modules are imported directly rather than via `runner`, which
    starts a suite run at import time. Every module exposing IMPLS is collected,
    so a module runner forgets to list is still checked.
    """

    def _merge_all(self):
        """Flatten every group module's registrations the way runner.py does,
        so both checks below see exactly the map a real run would build."""
        import importlib
        import pkgutil

        # An unimportable group module fails rather than skips. These two are
        # the only checks in the file that read the suite's *real*
        # registrations, and they used to call skipTest here — so on an
        # interpreter without boto3 they were reported as skipped and the run
        # still exited 0. A skip is indistinguishable from a pass at the exit
        # code, which is the same silently-do-the-wrong-thing failure the
        # loader validation these tests cover exists to refuse. CI ran them
        # that way until the dependency install landed beside this change.
        remedy = "run 'pip install -r requirements.txt' from compat/suites/python-sdk/"

        try:
            import groups as groups_pkg
        except ImportError as exc:
            self.fail(f"suite group modules unavailable: {exc}. To fix, {remedy}")

        sources = []
        for info in pkgutil.iter_modules(groups_pkg.__path__):
            try:
                module = importlib.import_module(f"groups.{info.name}")
            except ImportError as exc:
                self.fail(f"cannot import groups.{info.name}: {exc}. To fix, {remedy}")
            sources.append((module.__name__, getattr(module, "IMPLS", {})))

        merged = merge_impls(sources, "python-sdk")
        self.assertTrue(merged, "no IMPLS collected from the groups package")
        return merged

    def test_registered_impls_resolve(self):
        validate_impls(load_registry(), self._merge_all(), "python-sdk")

    def test_registered_impls_have_no_duplicate_keys(self):
        # merge_impls raises if two group modules claim the same key; the
        # discarded implementation would otherwise never run.
        self._merge_all()

    def test_registered_impls_have_no_bare_keys(self):
        # #1700: every impl key must be "<group>:<test>". A bare key resolves
        # only for as long as no registry group happens to declare the same
        # test name — the first generated group sharing a PascalCase operation
        # name with a hand-written one turns that bare key ambiguous and
        # aborts the run (compat/AGENTS.md § Implementation keys). Qualifying
        # every key up front means a new group can never make an existing
        # registration ambiguous again.
        merged = self._merge_all()
        bare = sorted(key for key in merged if ":" not in key)
        self.assertEqual(
            [], bare,
            f"{len(bare)} impl key(s) are not group-qualified: {bare}",
        )


class GeneratedRegistryLoading(unittest.TestCase):
    """
    registry.generated.json (#1393): a missing file is a no-op, an empty file
    is a no-op, and a non-empty one is concatenated onto the hand-written
    groups without disturbing them.

    These assert the *invariant* ("a missing/empty generated file changes
    nothing"), never a fact about the checked-in file's current contents —
    see the loader contract's note on not pinning a fact another in-flight
    branch (the compatgen generator) is about to change.
    """

    def test_missing_file_is_a_no_op(self):
        with tempfile.TemporaryDirectory() as tmp:
            missing = os.path.join(tmp, "does-not-exist.json")
            generated = load_generated_registry(missing)
            self.assertEqual({"version": 1, "groups": []}, generated)

    def test_missing_file_leaves_build_output_unchanged(self):
        registry = {
            "groups": [
                {"service": "s3", "name": "s3-crud", "tests": [{"name": "CreateBucket"}]},
            ]
        }
        with tempfile.TemporaryDirectory() as tmp:
            missing = os.path.join(tmp, "does-not-exist.json")
            with_generated = merge_registries(registry, load_generated_registry(missing))
            without = build_groups_from_registry(registry, {}, "python-sdk")
            merged = build_groups_from_registry(with_generated, {}, "python-sdk")
            self.assertEqual([g.name for g in without], [g.name for g in merged])

    def test_an_empty_file_is_a_no_op(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "registry.generated.json")
            with open(path, "w", encoding="utf-8") as f:
                json.dump({"version": 1, "groups": []}, f)
            self.assertEqual([], load_generated_registry(path)["groups"])

    def test_the_checked_in_file_loads_at_its_default_location(self):
        # The real, checked-in registry.generated.json, at its default
        # sibling-of-registry.json location. It was empty until the python-sdk
        # scenario backend landed (#1113 phase G2), and cmd/compatgen rewrites
        # it whenever a recipe, a scenario or the backend table changes — so
        # this asserts the shape the loader depends on, never the contents,
        # which another in-flight branch may be about to change.
        generated = load_generated_registry()
        self.assertEqual(1, generated["version"])
        for rg in generated["groups"]:
            for key in ("service", "name", "generated", "state", "suites", "tests"):
                self.assertIn(key, rg, msg=rg.get("name"))
            self.assertTrue(rg["suites"], msg=rg["name"])
            self.assertTrue(rg["tests"], msg=rg["name"])

    def test_synthetic_file_is_concatenated_after_hand_written(self):
        hand_written = {
            "groups": [
                {"service": "s3", "name": "s3-crud", "tests": [{"name": "CreateBucket"}]},
            ]
        }
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "registry.generated.json")
            with open(path, "w", encoding="utf-8") as f:
                json.dump({
                    "version": 1,
                    "groups": [
                        {
                            "service": "kinesis",
                            "name": "kinesis-streams",
                            "tests": [{"name": "CreateStream"}],
                            "generated": True,
                            "state": "candidate",
                            "suites": ["python-sdk"],
                        },
                    ],
                }, f)
            generated = load_generated_registry(path)
            merged = merge_registries(hand_written, generated)
            self.assertEqual(
                ["s3-crud", "kinesis-streams"],
                [g["name"] for g in merged["groups"]],
            )

    def test_present_but_unparsable_file_is_a_load_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "registry.generated.json")
            with open(path, "w", encoding="utf-8") as f:
                f.write("{not valid json")
            with self.assertRaises(json.JSONDecodeError):
                load_generated_registry(path)

    def test_wrong_version_is_a_load_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "registry.generated.json")
            with open(path, "w", encoding="utf-8") as f:
                json.dump({"version": 2, "groups": []}, f)
            with self.assertRaises(ValueError):
                load_generated_registry(path)

    def test_group_missing_required_fields_is_a_load_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "registry.generated.json")
            with open(path, "w", encoding="utf-8") as f:
                json.dump({
                    "version": 1,
                    "groups": [
                        {
                            "service": "kinesis",
                            "name": "kinesis-streams",
                            "tests": [{"name": "CreateStream"}],
                            # Missing generated/state/suites.
                        },
                    ],
                }, f)
            with self.assertRaises(ValueError) as cm:
                load_generated_registry(path)
            self.assertIn("kinesis-streams", str(cm.exception))

    def test_name_collision_between_files_is_a_load_error(self):
        hand_written = {
            "groups": [
                {"service": "s3", "name": "s3-crud", "tests": [{"name": "CreateBucket"}]},
            ]
        }
        generated = {
            "version": 1,
            "groups": [
                {
                    "service": "s3",
                    "name": "s3-crud",
                    "tests": [{"name": "PutObject"}],
                    "generated": True,
                    "state": "candidate",
                    "suites": ["python-sdk"],
                },
            ],
        }
        with self.assertRaises(ValueError) as cm:
            merge_registries(hand_written, generated)
        self.assertIn("s3-crud", str(cm.exception))


class GeneratedGroupSuitesScoping(unittest.TestCase):
    """A generated group's `suites` list is honoured exactly like a
    hand-written group's — a suite not named in it does not get the group at
    all: no tests, no skips, no results."""

    def _registry(self, suites):
        return {
            "groups": [
                {
                    "service": "kinesis",
                    "name": "kinesis-streams",
                    "tests": [{"name": "CreateStream"}],
                    "generated": True,
                    "state": "candidate",
                    "suites": suites,
                },
            ]
        }

    def test_out_of_scope_group_is_not_loaded_at_all(self):
        groups = build_groups_from_registry(
            self._registry(["go-sdk"]), {}, "python-sdk"
        )
        self.assertEqual([], groups)

    def test_in_scope_group_is_loaded(self):
        groups = build_groups_from_registry(
            self._registry(["python-sdk"]), {}, "python-sdk"
        )
        self.assertEqual(["kinesis-streams"], [g.name for g in groups])

    def test_hand_written_suites_scoping_unaffected(self):
        # cdk-lifecycle-shaped: a hand-written group scoped away from an SDK
        # suite must still be excluded, matching today's `service == "cdk"`
        # special case that this general check replaces.
        registry = {
            "groups": [
                {
                    "service": "cdk",
                    "name": "cdk-lifecycle",
                    "tests": [{"name": "DeployStack"}],
                    "suites": ["cdk"],
                },
                {
                    "service": "s3",
                    "name": "s3-crud",
                    "tests": [{"name": "CreateBucket"}],
                },
            ]
        }
        groups = build_groups_from_registry(registry, {}, "python-sdk")
        self.assertEqual(["s3-crud"], [g.name for g in groups])


def _only_test_result(buf: io.StringIO) -> dict:
    """The sole `test_result` event from a single-test group's captured
    stdout (a group also emits a `test_start` line first)."""
    for line in buf.getvalue().splitlines():
        event = json.loads(line)
        if event.get("event") == "test_result":
            return event
    raise AssertionError(f"no test_result event in output: {buf.getvalue()!r}")


class GeneratedGroupInterimFailRule(unittest.TestCase):
    """
    A generated group in scope with no registered impl and no scenario
    backend must FAIL, not skip and not na (#1393). Until the G2 interpreters
    land, this is the only signal that a suite named in a generated group's
    `suites` cannot actually run it.
    """

    def _registry(self, scenario=None):
        group = {
            "service": "kinesis",
            "name": "kinesis-streams",
            "tests": [{"name": "CreateStream"}],
            "generated": True,
            "state": "candidate",
            "suites": ["python-sdk"],
        }
        if scenario is not None:
            group["scenario"] = scenario
        return {"groups": [group]}

    def test_no_impl_no_backend_yields_fail_with_exact_message(self):
        groups = build_groups_from_registry(self._registry(), {}, "python-sdk")
        self.assertEqual(1, len(groups))
        tc = groups[0].tests[0]
        self.assertIsNone(tc.skip)
        self.assertIsNone(tc.na)

        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            counts = run_group(groups[0], TestContext("", "us-east-1", "test"))
        passed, failed, skipped, unimplemented, _cancelled = counts
        self.assertEqual((0, 1, 0, 0), (passed, failed, skipped, unimplemented))

        result = _only_test_result(buf)
        self.assertEqual("fail", result["status"])
        self.assertEqual(
            'generated group "kinesis-streams" is scoped to python-sdk but '
            "python-sdk has no scenario backend",
            result["error"],
        )

    def test_scenario_backend_is_consulted_first(self):
        seen = []

        def backend(group, test, scenario):
            seen.append((group, test, scenario))
            return lambda ctx: None  # a passing impl

        groups = build_groups_from_registry(
            self._registry(scenario="scenarios/kinesis.ir.json"),
            {},
            "python-sdk",
            scenario_backend=backend,
        )
        self.assertEqual(
            [("kinesis-streams", "CreateStream", "scenarios/kinesis.ir.json")], seen
        )

        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            run_group(groups[0], TestContext("", "us-east-1", "test"))
        result = _only_test_result(buf)
        self.assertEqual("pass", result["status"])

    def test_scenario_backend_declining_falls_back_to_fail(self):
        groups = build_groups_from_registry(
            self._registry(),
            {},
            "python-sdk",
            scenario_backend=lambda group, test, scenario: None,
        )
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            run_group(groups[0], TestContext("", "us-east-1", "test"))
        result = _only_test_result(buf)
        self.assertEqual("fail", result["status"])

    def test_hand_written_group_keeps_the_skip_sentinel(self):
        # Hand-written groups keep today's sentinel behaviour, byte-for-byte —
        # only `generated` groups get the new fail rule.
        registry = {
            "groups": [
                {"service": "s3", "name": "s3-crud", "tests": [{"name": "CreateBucket"}]},
            ]
        }
        groups = build_groups_from_registry(registry, {}, "python-sdk")
        tc = groups[0].tests[0]
        self.assertEqual("not yet implemented in python-sdk test suite", tc.skip)


if __name__ == "__main__":
    unittest.main()
