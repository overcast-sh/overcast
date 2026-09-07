#!/usr/bin/env python3
"""Tests for scripts/validate-compat-registry.py.

Two lints added for issue #1113 Phase G0 (docs/plans/compat-coverage-modelgen.md
§3.6, open questions §7.2 and §7.7):

1. Suites-scoping: a hand-written `compat/suites/registry.json` group may not
   declare `"suites"` outside the small allowed set (today just
   `cdk-lifecycle`); a *generated* group (one loaded from the sibling
   `compat/suites/registry.generated.json`, which cmd/compatgen owns) must
   always declare `"suites"`.
2. Service-key validation: every group's `service` must be a known Overcast
   capability service key (from `internal/capabilities/all.gen.go`), except
   the deliberate non-AWS `"cdk"` value used by `cdk-lifecycle`, and except a
   *generated* group naming a Tier 0 service the pruned shape snapshot covers
   (`models/aws/shapes-services.txt`) -- a G4 recipe that lands before the
   emulator implements the service, so no capability row exists for it yet.

The generated-registry half of lint 1 (and the generated-file leg of lint 2)
must tolerate `compat/suites/registry.generated.json` being absent: the file is
checked in, but a suite image or a branch cut before it existed has no copy.

Run: python3 scripts/validate_compat_registry_test.py
"""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("validate-compat-registry.py")
SPEC = importlib.util.spec_from_file_location("validate_compat_registry", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
vcr = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = vcr
SPEC.loader.exec_module(vcr)

# The schema check needs jsonschema; the two lints under test do not. See
# MainIntegrationTest for why that distinction is load-bearing here.
#
# find_spec returns None for a module that is simply absent, which is the CI
# case, but it propagates ImportError from a finder that refuses the name --
# so both mean "not usable here" and neither should be an error in a test file.
try:
    HAS_JSONSCHEMA = importlib.util.find_spec("jsonschema") is not None
except ImportError:  # pragma: no cover - depends on the environment
    HAS_JSONSCHEMA = False


def group(name, service="s3", suites=None, generated=None):
    g = {"service": service, "name": name, "tests": [{"name": "Get"}]}
    if suites is not None:
        g["suites"] = suites
    if generated is not None:
        g["generated"] = generated
    return g


class SuitesScopeHandWrittenTest(unittest.TestCase):
    """Hand-written registry.json groups: `suites` is reserved."""

    def test_unscoped_group_is_fine(self):
        registry = {"groups": [group("s3-crud")]}
        self.assertEqual(vcr.suites_scope_errors(registry), [])

    def test_cdk_lifecycle_is_the_allowed_exception(self):
        registry = {"groups": [group("cdk-lifecycle", service="cdk", suites=["cdk"])]}
        self.assertEqual(vcr.suites_scope_errors(registry), [])

    def test_sdk_suite_scoping_a_hand_written_group_is_rejected(self):
        # This is exactly the case compat/AGENTS.md's absolute rule existed to
        # forbid: an SDK suite is never a legitimate `suites` scope on a
        # hand-written group.
        registry = {"groups": [group("s3-crud", suites=["node-js-sdk"])]}
        errors = vcr.suites_scope_errors(registry)
        self.assertEqual(len(errors), 1)
        self.assertIn("s3-crud", errors[0])

    def test_real_registry_has_no_suites_scope_violations(self):
        registry = vcr.load_json(vcr.DEFAULT_REGISTRY)
        self.assertEqual(vcr.suites_scope_errors(registry), [])


class GeneratedRegistryTest(unittest.TestCase):
    """Both halves of the generated registry's presence.

    Absence still has to be tolerated -- a suite image, a CI artifact, or a
    maintenance branch cut before the file existed all read this lint's path
    without it. Presence is now the ordinary case: the sibling PR landed
    compat/suites/registry.generated.json, empty, and it is what
    cmd/compatgen will rewrite wholly.
    """

    def test_missing_generated_registry_is_not_an_error(self):
        with tempfile.TemporaryDirectory() as d:
            missing = Path(d) / "registry.generated.json"
            self.assertFalse(missing.exists())
            self.assertEqual(vcr.load_json_optional(missing), None)

    def test_real_generated_registry_loads_and_passes_its_checks(self):
        # This replaces an assertion that the file did not exist yet, which
        # was true only until the sibling PR merged and then failed on main.
        # A test may not pin a fact that another in-flight branch is about to
        # change; what is durable is that the checked-in file loads and is
        # clean, which is the invariant regeneration must preserve.
        loaded = vcr.load_json_optional(vcr.DEFAULT_GENERATED_REGISTRY)
        self.assertIsNotNone(
            loaded, f"{vcr.DEFAULT_GENERATED_REGISTRY} should be checked in"
        )
        keys = vcr.load_capability_service_keys(vcr.DEFAULT_CAPABILITIES)
        snapshot = vcr.load_shape_snapshot_service_keys(vcr.DEFAULT_SHAPES_SERVICES)
        self.assertEqual(vcr.generated_group_errors(loaded, keys, snapshot), [])


class GeneratedGroupMustDeclareSuitesTest(unittest.TestCase):
    def test_generated_group_without_suites_is_rejected(self):
        generated = {"groups": [group("dynamodb-generated-0001", generated=True)]}
        errors = vcr.generated_group_errors(
            generated, capability_keys={"dynamodb"}, snapshot_keys=set()
        )
        self.assertTrue(any("suites" in e for e in errors))

    def test_generated_group_with_suites_passes(self):
        generated = {
            "groups": [
                group(
                    "dynamodb-generated-0001",
                    service="dynamodb",
                    suites=["go-sdk", "python-sdk", "cli"],
                    generated=True,
                )
            ]
        }
        errors = vcr.generated_group_errors(
            generated, capability_keys={"dynamodb"}, snapshot_keys=set()
        )
        self.assertEqual(errors, [])


class GeneratedOnlyFieldTest(unittest.TestCase):
    """A hand-written group may carry `scenario`, and nothing else generated.

    #1903 item 1: `scenario` on a hand-written group is how it says its tests
    are resolved by an authored IR scenario through each suite's scenario
    backend, which is the whole of the flip. `generated`, `state`, `shadowOf`
    and `parallel` stay cmd/compatgen's: each states a fact about generator
    output, and a hand-written copy could only contradict it.
    """

    def test_scenario_is_allowed(self):
        registry = {"groups": [dict(group("sqs-queues"), scenario="compat/model/authored/sqs-queues.json")]}
        self.assertEqual(vcr.generated_only_field_errors(registry), [])

    def test_plain_group_is_allowed(self):
        self.assertEqual(vcr.generated_only_field_errors({"groups": [group("s3-crud")]}), [])

    def test_each_generated_only_field_is_rejected(self):
        for field, value in (
            ("generated", True),
            ("state", "candidate"),
            ("shadowOf", "s3-other"),
            ("parallel", True),
        ):
            with self.subTest(field=field):
                registry = {"groups": [dict(group("s3-crud"), **{field: value})]}
                errors = vcr.generated_only_field_errors(registry)
                self.assertEqual(len(errors), 1)
                self.assertIn(field, errors[0])
                self.assertIn("s3-crud", errors[0])

    def test_real_registry_carries_none_of_them(self):
        registry = vcr.load_json(vcr.DEFAULT_REGISTRY)
        self.assertEqual(vcr.generated_only_field_errors(registry), [])


class PortedGroupTest(unittest.TestCase):
    """A ported group and the index that scopes it must agree, both ways.

    #1903 item 2. `suites` on a ported group is derived from backend
    availability, exactly as it is on a generated group, so it is written into
    the generated sibling's `ported` index rather than onto the hand-written
    group. The two files are then one statement in two halves, and either half
    alone fails silently: a group with no entry is scoped to every uniform
    suite whether or not they can run it, an entry for a group that is not
    ported scopes nothing.
    """

    SCENARIO = "compat/model/authored/sqs-queues.json"

    @classmethod
    def hand(cls, scenario=SCENARIO):
        g = group("sqs-queues", service="sqs")
        if scenario is not None:
            g["scenario"] = scenario
        return {"groups": [g]}

    @classmethod
    def indexed(cls, group_name="sqs-queues", scenario=SCENARIO, suites=("cli", "go-sdk")):
        entry = {"group": group_name, "scenario": scenario}
        if suites is not None:
            entry["suites"] = list(suites)
        return {"groups": [], "ported": [entry]}

    def test_a_matching_pair_passes(self):
        self.assertEqual(vcr.ported_group_errors(self.hand(), self.indexed()), [])

    def test_a_registry_with_no_ported_group_and_an_empty_index_passes(self):
        self.assertEqual(vcr.ported_group_errors({"groups": [group("s3-crud")]}, {"groups": []}), [])

    def test_a_ported_group_with_no_index_entry_is_rejected(self):
        errors = vcr.ported_group_errors(self.hand(), {"groups": [group("sqs-gen-queue", generated=True)]})
        self.assertEqual(len(errors), 1)
        self.assertIn("sqs-queues", errors[0])
        self.assertIn("ported", errors[0])

    def test_an_empty_generated_registry_indexes_nothing_and_is_not_checked(self):
        # Phase G0's equivalence: absent and present-but-empty must reach the
        # same verdict, and neither can index anything.
        self.assertEqual(vcr.ported_group_errors(self.hand(), {"groups": []}), [])

    def test_an_entry_for_an_unported_group_is_rejected(self):
        errors = vcr.ported_group_errors(self.hand(scenario=None), self.indexed())
        self.assertTrue(any("sqs-queues" in e for e in errors))

    def test_disagreeing_scenario_paths_are_rejected(self):
        errors = vcr.ported_group_errors(
            self.hand(), self.indexed(scenario="compat/model/authored/other.json")
        )
        self.assertTrue(any("other.json" in e for e in errors))

    def test_an_entry_with_no_suites_is_rejected(self):
        errors = vcr.ported_group_errors(self.hand(), self.indexed(suites=None))
        self.assertTrue(any("suites" in e for e in errors))

    def test_a_duplicate_entry_is_rejected(self):
        generated = self.indexed()
        generated["ported"].append(dict(generated["ported"][0]))
        errors = vcr.ported_group_errors(self.hand(), generated)
        self.assertTrue(any("twice" in e for e in errors))

    def test_an_entry_colliding_with_a_generated_group_is_rejected(self):
        generated = self.indexed()
        generated["groups"] = [
            group("sqs-queues", service="sqs", suites=["cli"], generated=True)
        ]
        errors = vcr.ported_group_errors(self.hand(), generated)
        self.assertTrue(any("also a generated group" in e for e in errors))

    def test_the_real_pair_joins_on_sqs_queues(self):
        # sqs-queues is the first ported group (#1903 item 3): the authored
        # scenario replaced its seven native implementations, and the index
        # entry is where its `suites` comes from.
        registry = vcr.load_json(vcr.DEFAULT_REGISTRY)
        generated = vcr.load_json_optional(vcr.DEFAULT_GENERATED_REGISTRY)
        self.assertIsNotNone(generated)
        self.assertEqual(vcr.ported_group_errors(registry, generated), [])
        indexed = {p["group"]: p for p in generated.get("ported", [])}
        self.assertIn("sqs-queues", indexed)
        self.assertEqual(
            "compat/model/authored/sqs-queues.json", indexed["sqs-queues"]["scenario"]
        )
        self.assertEqual(7, len(indexed["sqs-queues"]["suites"]))


class ShadowGroupTest(unittest.TestCase):
    """A shadow group has to join the hand-written group it names.

    While a hand-written group is being ported to an authored IR scenario
    (docs/plans/compat-coverage-modelgen.md §3.11) the port runs beside the
    natives under `<group>-shadow`, and `cmd/compat --compare-shadow` joins the
    two on (suite, test). A shadow that names a group nobody declares, or whose
    test names have drifted, compares against nothing and reports agreement --
    which is the evidence a flip PR deletes seven implementations on.
    """

    @staticmethod
    def hand(tests=("CreateQueue", "DeleteQueue")):
        return {
            "groups": [
                {
                    "service": "sqs",
                    "name": "sqs-queues",
                    "tests": [{"name": t} for t in tests],
                }
            ]
        }

    @staticmethod
    def shadow(tests=("CreateQueue", "DeleteQueue"), state="candidate", shadow_of="sqs-queues"):
        return {
            "groups": [
                {
                    "service": "sqs",
                    "name": "sqs-queues-shadow",
                    "generated": True,
                    "state": state,
                    "shadowOf": shadow_of,
                    "suites": ["cli", "go-sdk"],
                    "tests": [{"name": t} for t in tests],
                }
            ]
        }

    def test_matching_shadow_passes(self):
        self.assertEqual(vcr.shadow_group_errors(self.hand(), self.shadow()), [])

    def test_shadow_of_an_unknown_group_is_rejected(self):
        errors = vcr.shadow_group_errors(self.hand(), self.shadow(shadow_of="sqs-nothing"))
        self.assertTrue(any("not a group in registry.json" in e for e in errors), errors)

    def test_shadow_missing_a_native_test_is_rejected(self):
        errors = vcr.shadow_group_errors(self.hand(), self.shadow(tests=("CreateQueue",)))
        self.assertTrue(any("does not declare DeleteQueue" in e for e in errors), errors)

    def test_shadow_with_an_extra_test_is_rejected(self):
        errors = vcr.shadow_group_errors(
            self.hand(), self.shadow(tests=("CreateQueue", "DeleteQueue", "PurgeQueue"))
        )
        self.assertTrue(any("declares PurgeQueue" in e for e in errors), errors)

    def test_gated_shadow_is_rejected(self):
        errors = vcr.shadow_group_errors(self.hand(), self.shadow(state="gated"))
        self.assertTrue(any("gates nothing" in e for e in errors), errors)

    def test_a_generated_group_without_shadow_of_is_not_checked(self):
        generated = {
            "groups": [
                {
                    "service": "sqs",
                    "name": "sqs-gen-queue",
                    "generated": True,
                    "state": "gated",
                    "suites": ["cli"],
                    "tests": [{"name": "CreateQueue"}],
                }
            ]
        }
        self.assertEqual(vcr.shadow_group_errors(self.hand(), generated), [])

    def test_the_committed_pair_is_consistent(self):
        registry = json.loads(vcr.DEFAULT_REGISTRY.read_text(encoding="utf-8"))
        generated = vcr.load_json_optional(vcr.DEFAULT_GENERATED_REGISTRY)
        if generated is None:
            self.skipTest("no generated registry in this checkout")
        self.assertEqual(vcr.shadow_group_errors(registry, generated), [])


class ServiceKeyValidationTest(unittest.TestCase):
    def test_known_capability_key_passes(self):
        registry = {"groups": [group("s3-crud", service="s3")]}
        errors = vcr.service_key_errors(registry, capability_keys={"s3", "sqs"})
        self.assertEqual(errors, [])

    def test_unknown_service_key_is_rejected(self):
        registry = {"groups": [group("bogus-crud", service="not-a-real-service")]}
        errors = vcr.service_key_errors(registry, capability_keys={"s3", "sqs"})
        self.assertEqual(len(errors), 1)
        self.assertIn("not-a-real-service", errors[0])

    def test_cdk_service_on_cdk_lifecycle_is_the_deliberate_exception(self):
        registry = {"groups": [group("cdk-lifecycle", service="cdk", suites=["cdk"])]}
        errors = vcr.service_key_errors(registry, capability_keys={"s3", "sqs"})
        self.assertEqual(errors, [])

    def test_cdk_service_on_any_other_group_is_still_rejected(self):
        # "cdk" is a deliberate, narrow exception for cdk-lifecycle -- not a
        # general escape hatch for "not an AWS service".
        registry = {"groups": [group("some-other-group", service="cdk")]}
        errors = vcr.service_key_errors(registry, capability_keys={"s3", "sqs"})
        self.assertEqual(len(errors), 1)

    def test_real_registry_has_no_service_key_violations(self):
        registry = vcr.load_json(vcr.DEFAULT_REGISTRY)
        capability_keys = vcr.load_capability_service_keys(vcr.DEFAULT_CAPABILITIES)
        self.assertEqual(vcr.service_key_errors(registry, capability_keys), [])


class Tier0GeneratedServiceKeyTest(unittest.TestCase):
    """The G4 widening: a generated group may name a Tier 0 service.

    A recipe for a service Overcast has not implemented lands before the
    emulator does -- that is what G4 is -- so the service has no capability
    row and the §7.7 assumption ("generated groups use the capability key by
    construction") has nothing to check against. The widening is conditioned
    on the snapshot's own reviewed service list, and it does not reach
    hand-written groups.
    """

    def test_tier0_service_passes_for_a_generated_group(self):
        generated = {
            "groups": [
                group(
                    "batch-gen-jobqueue",
                    service="batch",
                    suites=["go-sdk", "python-sdk", "cli"],
                    generated=True,
                )
            ]
        }
        errors = vcr.generated_group_errors(
            generated, capability_keys={"s3", "sqs"}, snapshot_keys={"batch"}
        )
        self.assertEqual(errors, [])

    def test_service_outside_the_snapshot_is_still_rejected(self):
        generated = {
            "groups": [
                group(
                    "bogus-gen-thing",
                    service="not-a-real-service",
                    suites=["cli"],
                    generated=True,
                )
            ]
        }
        errors = vcr.generated_group_errors(
            generated, capability_keys={"s3", "sqs"}, snapshot_keys={"batch"}
        )
        self.assertEqual(len(errors), 1)
        self.assertIn("not-a-real-service", errors[0])

    def test_hand_written_group_gets_no_tier0_widening(self):
        # service_key_errors is called without tier0_keys for registry.json,
        # so a hand-written group naming a Tier 0 service is still an error:
        # there is nothing for a hand-written group to test against a service
        # the emulator does not implement.
        registry = {"groups": [group("batch-crud", service="batch")]}
        errors = vcr.service_key_errors(registry, capability_keys={"s3", "sqs"})
        self.assertEqual(len(errors), 1)


class ShapeSnapshotServiceKeyParsingTest(unittest.TestCase):
    def test_parses_keys_and_ignores_comments_and_blanks(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "shapes-services.txt"
            path.write_text(
                "\n".join(
                    [
                        "# a comment",
                        "",
                        "batch                  # REST-JSON",
                        "elastic-load-balancing # AWS Query",
                        "sqs",
                        "",
                    ]
                ),
                encoding="utf-8",
            )
            self.assertEqual(
                vcr.load_shape_snapshot_service_keys(path),
                {"batch", "elastic-load-balancing", "sqs"},
            )

    def test_missing_file_is_an_empty_set_rather_than_a_failure(self):
        with tempfile.TemporaryDirectory() as d:
            self.assertEqual(
                vcr.load_shape_snapshot_service_keys(Path(d) / "nope.txt"), set()
            )

    def test_real_file_parses_to_a_nonempty_set(self):
        keys = vcr.load_shape_snapshot_service_keys(vcr.DEFAULT_SHAPES_SERVICES)
        self.assertIn("sqs", keys)
        self.assertIn("organizations", keys)


class CapabilityServiceKeyParsingTest(unittest.TestCase):
    def test_parses_service_keys_from_generated_capabilities_snapshot(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "all.gen.go"
            path.write_text(
                'package capabilities\n\n'
                'var AllCapabilities = []Capability{\n'
                '\t{Service: "s3", Operation: "CreateBucket", Category: "Buckets"},\n'
                '\t{Service: "s3", Operation: "DeleteBucket", Category: "Buckets"},\n'
                '\t{Service: "sqs", Operation: "CreateQueue", Category: "Queues"},\n'
                '}\n',
                encoding="utf-8",
            )
            keys = vcr.load_capability_service_keys(path)
            self.assertEqual(keys, {"s3", "sqs"})

    def test_real_capabilities_file_parses_to_a_nonempty_set(self):
        keys = vcr.load_capability_service_keys(vcr.DEFAULT_CAPABILITIES)
        self.assertIn("s3", keys)
        self.assertIn("dynamodb", keys)
        self.assertGreater(len(keys), 20)


@unittest.skipUnless(HAS_JSONSCHEMA, "jsonschema is not installed")
class MainIntegrationTest(unittest.TestCase):
    """main() wires both new lints in alongside the existing schema check.

    Skipped without jsonschema. These are the only tests here that reach the
    schema check, and the CI job that runs scripts/*_test.py installs no pip
    dependencies on purpose -- the script tests are meant to stay cheap. The
    lints themselves are pure Python and their unit tests above run
    unconditionally; end-to-end coverage of main() against the real registry
    is what the `Script tests` job already does, with jsonschema installed.
    """

    def test_main_still_passes_on_the_real_registry(self):
        self.assertEqual(vcr.main([]), 0)

    def test_main_fails_on_a_bad_suites_scope(self):
        with tempfile.TemporaryDirectory() as d:
            registry_path = Path(d) / "registry.json"
            registry_path.write_text(
                json.dumps(
                    {
                        "version": 1,
                        "groups": [
                            {
                                "service": "s3",
                                "name": "s3-crud",
                                "suites": ["node-js-sdk"],
                                "tests": [{"name": "Get"}],
                            }
                        ],
                    }
                ),
                encoding="utf-8",
            )
            # The generated registry is pointed away from the checked-in one:
            # its shadow groups name hand-written groups this fixture does not
            # have, so leaving it in would make the case pass on the shadow
            # lint rather than on the suites-scope lint it is about.
            rc = vcr.main(
                [
                    "--registry",
                    str(registry_path),
                    "--schema",
                    str(vcr.DEFAULT_SCHEMA),
                    "--generated-registry",
                    str(Path(d) / "absent.json"),
                ]
            )
            self.assertEqual(rc, 1)


if __name__ == "__main__":
    unittest.main()
