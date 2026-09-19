#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import re
import sys
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("ci-scope.py")
SPEC = importlib.util.spec_from_file_location("ci_scope", SCRIPT)
assert SPEC is not None
scope = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
# Registered before exec, as changelog_required_test does: a hyphenated
# filename cannot be imported any other way.
sys.modules["ci_scope"] = scope
SPEC.loader.exec_module(scope)


class NeedsCodeJobs(unittest.TestCase):
	"""The decision this file exists to protect.

	A wrong `True` costs a CI run. A wrong `False` skips the suite on a change
	that needed it — a green tick over untested code — so every ambiguous case
	below asserts `True`.
	"""

	def test_prose_only_skips(self) -> None:
		for files in (
			["AGENTS.md"],
			["docs/plans/ci-streamlining.md"],
			["docs/dev/development-setup.md"],
			[".changelog/20260804-something.md"],
			["README.md", "CONTRIBUTING.md", ".changelog/x.md"],
		):
			with self.subTest(files=files):
				self.assertFalse(scope.needs_code_jobs(files))

	def test_any_code_file_runs_everything(self) -> None:
		for files in (
			["internal/services/rds/password.go"],
			["Dockerfile"],
			[".github/workflows/test.yml"],
			["scripts/verify-changed.sh"],
			["web/src/main.tsx"],
			["go.mod"],
		):
			with self.subTest(files=files):
				self.assertTrue(scope.needs_code_jobs(files))

	def test_a_mixed_change_runs_everything(self) -> None:
		# The case that breaks the twin-workflow approach, and the reason this
		# is a classifier: one doc plus one handler is a code change.
		self.assertTrue(scope.needs_code_jobs(["docs/services/rds.md", "internal/services/rds/password.go"]))

	def test_a_published_doc_is_code(self) -> None:
		# embed.go compiles published docs into the binary and
		# internal/docsindex indexes them, so a published doc is a build input
		# and the corpus tests assert on it. Treating one as prose would skip
		# the suite that has to accept it.
		for files in (
			["docs/services/rds.md"],
			["docs/README.md"],
			["docs/cdk/local-vpc.md"],
			["docs/generated/service-support.json"],
		):
			with self.subTest(files=files):
				self.assertTrue(scope.needs_code_jobs(files))

	def test_a_contributor_doc_is_not(self) -> None:
		# docs/plans/ and docs/dev/ are excluded from the embed pattern and the
		# index, so nothing reads them.
		self.assertFalse(scope.needs_code_jobs(["docs/dev/content-charter.md", "docs/plans/x.md"]))

	def test_compat_baseline_is_prose(self) -> None:
		# compat.yml opens a PR after every push to main that touches only
		# compat/baseline/*.json (a status ledger nothing builds or tests
		# against). Classifying it as code runs the entire test.yml and
		# compat.yml on a JSON-only diff for no reason.
		self.assertFalse(scope.needs_code_jobs(["compat/baseline/cli.json"]))
		self.assertFalse(scope.needs_code_jobs(["compat/baseline/java-sdk.json", "compat/baseline/go-sdk.json"]))

	def test_the_tells_allowlist_is_code(self) -> None:
		# It lives under docs/dev/, but `make docs-lint` reads it and fails on an
		# entry that no longer matches, so it has to run the docs gate.
		self.assertFalse(scope.is_prose("docs/dev/llm-tells-allowlist.txt"))
		self.assertTrue(scope.needs_code_jobs(["docs/dev/llm-tells-allowlist.txt"]))

	def test_markdown_outside_docs_is_still_prose(self) -> None:
		self.assertFalse(scope.needs_code_jobs(["compat/AGENTS.md", "tests/AGENTS.md"]))

	def test_empty_change_set_runs_everything(self) -> None:
		# Not knowing what changed is not the same as knowing nothing did.
		self.assertTrue(scope.needs_code_jobs([]))

	def test_a_go_file_named_like_docs_is_code(self) -> None:
		# docs/ is a prefix match, not a substring one.
		self.assertFalse(scope.is_prose("docs_search.go"))
		self.assertFalse(scope.is_prose("internal/docssearch/index.gen.go"))


class ChangesImage(unittest.TestCase):
	"""Whether a pull request is worth a release candidate.

	The bias mirrors NeedsCodeJobs: a wrong `True` builds a candidate for
	nothing, a wrong `False` leaves a release PR without one, so every ambiguous
	case asserts `True`.
	"""

	def test_changes_that_cannot_reach_an_image_do_not_get_one(self) -> None:
		for files in (
			# The case that motivated it: a baseline promotion.
			["compat/baseline/cli.json", "compat/baseline/go-sdk.json"],
			# Tests never reach the binary, whatever tree they sit in.
			["internal/services/sqs/queue_test.go"],
			["internal/services/sqs/testdata/queue.json"],
			["tests/integration/sqs/queue_test.go"],
			["compat/suites/go-sdk/main.go"],
			["cmd/compat/main.go"],
			# Other binaries cannot be imported into cmd/overcast.
			["cmd/overcast-mcp/main.go"],
			[".github/workflows/test.yml"],
			["scripts/verify-changed.sh"],
			["docs/plans/ci-streamlining.md"],
			["docs/dev/testing.md"],
			["docs/generated/service-support.json"],
			["README.md", "RELEASE.md", "CHANGELOG.md", ".changelog/20260914-x.md"],
			["Makefile"],
		):
			with self.subTest(files=files):
				self.assertFalse(scope.changes_image(files))

	def test_an_input_of_the_image_gets_one(self) -> None:
		for files in (
			["cmd/overcast/main.go"],
			["internal/services/sqs/queue.go"],
			["web/src/main.tsx"],
			["web/src/main.test.tsx"],  # web tests are deliberately not excluded
			["docker/entrypoint.sh"],
			["Dockerfile"],
			[".dockerignore"],
			["go.mod"],
			["go.sum"],
			["embed.go"],
			["embed_slim.go"],
			["docs/README.md"],  # docs/*.md is embedded
			["docs/services/rds.md"],
			["docs/cdk/local-vpc.md"],
		):
			with self.subTest(files=files):
				self.assertTrue(scope.changes_image(files))

	def test_the_release_pr_always_gets_one(self) -> None:
		# VERSION names the candidate, and the release PR is the one thing that
		# must never miss it, whatever else it carries.
		self.assertTrue(scope.changes_image(["VERSION", "CHANGELOG.md", ".changelog/a.md", ".changelog/b.md"]))

	def test_a_mixed_change_gets_one(self) -> None:
		self.assertTrue(scope.changes_image(["compat/baseline/cli.json", "internal/router/router.go"]))

	def test_empty_change_set_gets_one(self) -> None:
		self.assertTrue(scope.changes_image([]))

	def test_docs_markdown_is_matched_one_level_deep_only(self) -> None:
		# The hash's `docs/*.md` is a shell glob, not a recursive one.
		self.assertTrue(scope.reaches_image("docs/README.md"))
		self.assertFalse(scope.reaches_image("docs/plans/x.md"))
		self.assertFalse(scope.reaches_image("docs/dev/x.md"))
		self.assertFalse(scope.reaches_image("docs/notes.txt"))

	def test_prefixes_are_not_substring_matches(self) -> None:
		self.assertFalse(scope.reaches_image("cmd/overcast-mcp/main.go"))
		self.assertFalse(scope.reaches_image("internalx/y.go"))
		self.assertFalse(scope.reaches_image("website/index.html"))


class ImageListMatchesTheHash(unittest.TestCase):
	"""`reaches_image` restates the paths test.yml hashes; fail if they drift.

	The hash is what decides whether two builds are the same image, so a path
	added there but not here would let a PR change an image and be told it has
	nothing to build a candidate for.
	"""

	def test_the_pathspecs_are_the_same(self) -> None:
		workflow = Path(__file__).resolve().parents[1] / ".github" / "workflows" / "test.yml"
		text = workflow.read_text(encoding="utf-8")
		# The pathspecs run from `git ls-tree -r HEAD --` to the `| grep -vE`
		# that drops tests, with `\` line continuations between them.
		match = re.search(r"git ls-tree -r HEAD --([^|]*)\|\s*grep -vE", text)
		self.assertIsNotNone(match, "the hash step in test.yml no longer has the shape this test reads")
		assert match is not None
		hashed = set(match.group(1).replace("\\", " ").split())

		restated = {p.rstrip("/") for p in scope.IMAGE_PREFIXES}
		restated |= {f for f in scope.IMAGE_FILES if f != "VERSION"}
		restated.add("docs/*.md")
		self.assertEqual(hashed, restated)


class IsProse(unittest.TestCase):
	def test_suffix_and_prefix(self) -> None:
		self.assertTrue(scope.is_prose("CONTRIBUTING.md"))
		self.assertTrue(scope.is_prose("docs/dev/testing.md"))
		self.assertFalse(scope.is_prose("docs/generated/service-support.json"))
		self.assertFalse(scope.is_prose("internal/router/router.go"))
		self.assertFalse(scope.is_prose("Makefile"))


if __name__ == "__main__":
	unittest.main()
