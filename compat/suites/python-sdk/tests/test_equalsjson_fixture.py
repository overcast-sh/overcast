"""
The shared `equalsJSON` conformance fixture, compat/model/testdata/equalsjson.

`equalsJSON` compares a member holding a JSON document by value rather than by
text (compat/model/README.md § Documents in a string). botocore's
json_decode_policies hands this suite an IAM policy document already decoded to
a dict, while five other backends see the percent-encoded string, so this is
where python-sdk proves it reaches the same verdict as all of them: the
percent-decoding, the documents that are equal and the ones that are not, and
the operands the loader refuses.

Run with:  python -m unittest discover -s tests  (from compat/suites/python-sdk/)
"""

from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from lib.registry import REPO_ROOT  # noqa: E402
from lib.scenario.expressions import check_equals_json_operand, percent_decode  # noqa: E402
from lib.scenario.failures import ScenarioError, ScenarioFailure  # noqa: E402
from lib.scenario.loader import ScenarioLibrary, _index  # noqa: E402

from test_scenario import CLIENT, FakeClient, _stderr, make_group, run_one  # noqa: E402

FIXTURE = os.path.join(REPO_ROOT, "compat", "model", "testdata", "equalsjson", "equalsjson.json")

# Every section's case keys, so a key this reader does not know — a new field
# every backend is meant to honour — fails here rather than being ignored.
_SECTIONS = {
    "decode": {"name", "text", "decoded"},
    "holds": {"name", "actual", "expected"},
    "fails": {"name", "actual", "expected"},
    "invalidExpected": {"name", "expected"},
}


def load_fixture() -> dict:
    with open(FIXTURE, encoding="utf-8") as fh:
        fixture = json.load(fh)
    unknown = set(fixture) - {"$comment", *_SECTIONS}
    if unknown:
        raise AssertionError(f"unknown keys in {FIXTURE}: {sorted(unknown)}")
    for section, keys in _SECTIONS.items():
        if not fixture.get(section):
            raise AssertionError(f"the equalsJSON fixture may not be skipped by emptying {section!r}")
        for case in fixture[section]:
            extra = set(case) - keys
            if extra:
                raise AssertionError(
                    f"unknown keys in {section} case {case.get('name')!r}: {sorted(extra)}")
    return fixture


def response_with(actual) -> dict:
    """A response whose `$.Doc` is the case's actual value, as boto3 would
    hand it over."""
    return {"Doc": actual}


def check_group(expected) -> object:
    return make_group(name="iam-gen-role", tests=[
        {"name": "GetRole", "op": "GetRole",
         "call": {"op": "GetRole", "params": {"RoleName": "r"}},
         "assert": [{"kind": "responseField",
                     "checks": {"$.Doc": {"equalsJSON": expected}}}]},
    ])


def run_check(actual, expected) -> None:
    """Run one equalsJSON check through the interpreter against a fake
    client. Returns when it holds; raises ScenarioFailure when it does not."""
    run_one(check_group(expected), "GetRole", FakeClient({"get_role": [response_with(actual)]}))


class TestSharedEqualsJsonFixture(unittest.TestCase):
    def test_decode(self):
        for case in load_fixture()["decode"]:
            with self.subTest(case["name"]):
                self.assertEqual(percent_decode(case["text"]), case["decoded"])

    def test_holds(self):
        for case in load_fixture()["holds"]:
            with self.subTest(case["name"]):
                run_check(case["actual"], case["expected"])

    def test_fails(self):
        for case in load_fixture()["fails"]:
            with self.subTest(case["name"]):
                with self.assertRaises(ScenarioFailure):
                    run_check(case["actual"], case["expected"])

    def test_invalid_expected_is_refused(self):
        for case in load_fixture()["invalidExpected"]:
            with self.subTest(case["name"]):
                with self.assertRaises(ScenarioError):
                    check_equals_json_operand(case["expected"])


class TestEqualsJsonFailureMessage(unittest.TestCase):
    def failure(self, actual, expected) -> str:
        with self.assertRaises(ScenarioFailure) as raised:
            run_check(actual, expected)
        return str(raised.exception)

    def test_a_document_that_differs_is_shown_decoded(self):
        message = self.failure('{"a":1,"b":2}', {"a": 1})
        self.assertIn('path=$.Doc expected=equalsJSON {"a":1} actual=document {"a":1,"b":2} ', message)

    def test_a_value_that_is_not_a_document_is_shown_as_it_came(self):
        message = self.failure("not a policy", {})
        self.assertIn('expected=equalsJSON {} actual=not a JSON document: "not a policy" ', message)

    def test_a_path_that_does_not_resolve_is_missing(self):
        with self.assertRaises(ScenarioFailure) as raised:
            run_one(check_group({}), "GetRole", FakeClient({"get_role": [{}]}))
        self.assertIn("expected=equalsJSON {} actual=<missing> ", str(raised.exception))

    def test_an_operand_that_slips_past_the_loader_fails_the_clause(self):
        # make_group does not go through the loader; the check still refuses
        # to read a `$` key rather than evaluate or ignore it.
        with self.assertRaises(ScenarioFailure) as raised:
            run_check("{}", {"$ref": "role.policy"})
        self.assertIn("never evaluated", str(raised.exception))


class TestEqualsJsonLoader(unittest.TestCase):
    def scenario(self, operand) -> dict:
        return {
            "version": 1, "service": "iam", "client": CLIENT,
            "groups": [{
                "name": "iam-gen-role", "kind": "lifecycle", "setup": [], "teardown": [],
                "tests": [{"name": "GetRole", "op": "GetRole",
                           "call": {"op": "GetRole", "params": {}},
                           "assert": [{"kind": "eventually", "maxAttempts": 2,
                                       "assert": {"kind": "readback",
                                                  "call": {"op": "GetRole", "params": {}},
                                                  "checks": {"$.Doc": {"equalsJSON": operand}}}}]}],
            }],
        }

    def test_a_literal_document_loads(self):
        self.assertIn("iam-gen-role", _index("f.json", self.scenario({"Version": "2012-10-17"})))

    def test_a_string_operand_is_refused(self):
        with self.assertRaisesRegex(ScenarioError, "object or array"):
            _index("f.json", self.scenario('{"a":1}'))

    def test_a_nested_dollar_key_is_refused(self):
        with self.assertRaisesRegex(ScenarioError, r"\$name"):
            _index("f.json", self.scenario({"Statement": [{"Resource": {"$name": "q"}}]}))

    def test_the_library_refuses_the_whole_file(self):
        with tempfile.TemporaryDirectory() as root:
            rel = "compat/model/scenarios/iam.json"
            os.makedirs(os.path.join(root, "compat", "model", "scenarios"))
            with open(os.path.join(root, *rel.split("/")), "w", encoding="utf-8") as f:
                json.dump(self.scenario({"$ref": "role.policy"}), f)
            library = ScenarioLibrary(root)
            errors = _stderr(lambda: self.assertIsNone(library.group(rel, "iam-gen-role")))
        self.assertIn("cannot read scenario file", errors)
        self.assertIn("$ref", errors)


if __name__ == "__main__":
    unittest.main()
