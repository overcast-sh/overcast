#!/usr/bin/env python3
"""issue_claim_test.py — tests for scripts/issue-claim.sh's claim step (#2094).

The claim used to run `gh issue edit --add-label status/in-progress` and never
remove the lifecycle label the issue already had, so #2062, #2063, #2064,
#2067 and #2088 all ended up carrying status/ready and status/in-progress at
once and had to be relabelled by hand. Lifecycle labels are a state machine
(.agents/skills/github-issue-lifecycle/SKILL.md § Updating Lifecycle State):
the claim has to leave the old state in the same edit it enters the new one.

These run the real script against a stub `gh` placed first on PATH. The stub
answers the GraphQL query from a canned response and records every
`gh issue edit`. Like the real gh, it fails the whole edit when asked to remove
a label the issue does not carry, so a claim that passes --remove-label
blindly fails here too. jq is the real one, because the script's decisions are
jq programs and a stub would only test the stub.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "issue-claim.sh"


def _under_system_root(path: Path) -> bool:
	"""True for the WSL launcher shipped in C:\\Windows\\System32."""
	if os.name != "nt":
		return False
	try:
		path.resolve().relative_to(Path(os.environ.get("SystemRoot", r"C:\Windows")).resolve())
	except (ValueError, OSError):
		return False
	return True


def find_bash() -> str:
	"""Git for Windows' bash ahead of the WSL launcher; PATH's bash elsewhere.

	See find_bash in pr-wait_test.py for why shutil.which alone picks the
	wrong one on a stock Windows install.
	"""
	candidates = []
	git = shutil.which("git")
	if git:
		root = Path(git).resolve().parent.parent
		candidates += [root / "bin" / "bash.exe", root / "usr" / "bin" / "bash.exe"]
	found = shutil.which("bash")
	if found:
		candidates.append(Path(found))
	for candidate in candidates:
		if candidate.is_file() and not _under_system_root(candidate):
			return str(candidate)
	return "bash"


BASH = find_bash()

# The stub reads everything it needs from files beside itself, so each test
# describes the issue as data and the stub stays the same. `issue edit`
# records one argument per line and a --- separator per call.
STUB_GH = r"""#!/usr/bin/env bash
here=$(cd "$(dirname "$0")" && pwd)
case "$1 $2" in
"auth status") exit 0 ;;
"repo view") echo overcast-sh/overcast; exit 0 ;;
"api graphql") cat "$here/response.json"; exit 0 ;;
"issue edit")
	shift 2
	{ printf '%s\n' "$@"; echo ---; } >>"$here/edits"
	[ -f "$here/edit-fails" ] && exit 1
	prev=""
	for a in "$@"; do
		if [ "$prev" = --remove-label ] && ! grep -qxF -- "$a" "$here/labels"; then
			echo "failed to update: label $a not on issue" >&2
			exit 1
		fi
		prev=$a
	done
	exit 0
	;;
esac
echo "stub gh: unexpected call: $*" >&2
exit 1
"""


def response(labels, state="OPEN", prs=()):
	return {
		"data": {
			"repository": {
				"issue": {
					"state": state,
					"title": "the issue",
					"labels": {"nodes": [{"name": name} for name in labels]},
					"timelineItems": {"nodes": [{"source": pr} for pr in prs]},
				}
			}
		}
	}


def closing_pr(number, issue, branch):
	return {
		"number": number,
		"title": "someone else's fix",
		"state": "OPEN",
		"isDraft": False,
		"url": f"https://github.com/overcast-sh/overcast/pull/{number}",
		"headRefName": branch,
		"closingIssuesReferences": {"nodes": [{"number": issue}]},
	}


@unittest.skipUnless(shutil.which("jq"), "jq is not installed")
class IssueClaimTest(unittest.TestCase):
	ISSUE = 4242

	def setUp(self) -> None:
		self.tmp = Path(tempfile.mkdtemp())
		self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
		self.bin = self.tmp / "bin"
		self.bin.mkdir()
		stub = self.bin / "gh"
		# newline="\n": a CRLF shebang line is not a runnable script under bash.
		stub.write_text(STUB_GH, encoding="utf-8", newline="\n")
		stub.chmod(0o755)
		# Not a git checkout, so the script's own branch lookup finds nothing
		# and every PR in a canned response belongs to someone else.
		self.cwd = self.tmp / "work"
		self.cwd.mkdir()

	def given(self, labels, **kwargs) -> None:
		(self.bin / "labels").write_text("".join(f"{name}\n" for name in labels), encoding="utf-8", newline="\n")
		(self.bin / "response.json").write_text(json.dumps(response(labels, **kwargs)), encoding="utf-8")

	def run_claim(self, *args: str) -> subprocess.CompletedProcess:
		env = dict(os.environ)
		env["PATH"] = str(self.bin) + os.pathsep + env.get("PATH", "")
		return subprocess.run(
			[BASH, str(SCRIPT), *args, str(self.ISSUE)],
			cwd=self.cwd, env=env, capture_output=True, text=True, encoding="utf-8",
		)

	def edits(self) -> list[list[str]]:
		path = self.bin / "edits"
		if not path.exists():
			return []
		calls, current = [], []
		for line in path.read_text(encoding="utf-8").splitlines():
			if line == "---":
				calls.append(current)
				current = []
			else:
				current.append(line)
		return calls

	def removed(self, call: list[str]) -> list[str]:
		return [call[i + 1] for i, a in enumerate(call) if a == "--remove-label"]

	def assert_one_claim(self, result: subprocess.CompletedProcess) -> list[str]:
		self.assertEqual(result.returncode, 0, result.stderr)
		calls = self.edits()
		self.assertEqual(len(calls), 1, f"expected exactly one gh issue edit, got {calls}")
		call = calls[0]
		self.assertEqual(call[0], str(self.ISSUE))
		self.assertIn("status/in-progress", [call[i + 1] for i, a in enumerate(call) if a == "--add-label"])
		self.assertIn("@me", [call[i + 1] for i, a in enumerate(call) if a == "--add-assignee"])
		self.assertIn("claimed #4242", result.stderr)
		return call

	def test_a_ready_issue_leaves_ready_in_the_same_edit(self) -> None:
		self.given(["bug", "status/ready", "priority/p1"])
		result = self.run_claim()
		call = self.assert_one_claim(result)
		self.assertEqual(self.removed(call), ["status/ready"])
		self.assertIn("removed status/ready", result.stderr)

	def test_every_pre_work_state_present_is_removed(self) -> None:
		self.given(["status/needs-triage", "status/blocked"])
		call = self.assert_one_claim(self.run_claim())
		self.assertEqual(sorted(self.removed(call)), ["status/blocked", "status/needs-triage"])

	def test_an_issue_with_no_lifecycle_label_removes_nothing(self) -> None:
		# The real gh fails the whole edit on removing an absent label, and so
		# does the stub: this is the case a blind --remove-label would break.
		self.given(["bug", "area/devex"])
		call = self.assert_one_claim(self.run_claim())
		self.assertEqual(self.removed(call), [])

	def test_the_already_doubled_state_is_repaired_on_reclaim(self) -> None:
		self.given(["status/ready", "status/in-progress"])
		call = self.assert_one_claim(self.run_claim())
		self.assertEqual(self.removed(call), ["status/ready"])

	def test_later_lifecycle_states_are_not_touched(self) -> None:
		# needs-review and done are not pre-work states; a claim does not
		# decide what they mean, so it leaves them for a person to resolve.
		self.given(["status/needs-review"])
		call = self.assert_one_claim(self.run_claim())
		self.assertEqual(self.removed(call), [])

	def test_check_never_edits(self) -> None:
		self.given(["status/ready"])
		result = self.run_claim("--check")
		self.assertEqual(result.returncode, 0, result.stderr)
		self.assertIn("is unclaimed", result.stderr)
		self.assertEqual(self.edits(), [])

	def test_a_closed_issue_is_a_conflict_and_is_not_edited(self) -> None:
		self.given(["status/ready"], state="CLOSED")
		result = self.run_claim()
		self.assertEqual(result.returncode, 3, result.stderr)
		self.assertEqual(self.edits(), [])

	def test_a_closing_pr_elsewhere_is_a_conflict_and_is_not_edited(self) -> None:
		self.given(["status/ready"], prs=[closing_pr(99, self.ISSUE, "claude/issue-4242-other")])
		result = self.run_claim()
		self.assertEqual(result.returncode, 3, result.stderr)
		self.assertIn("#99", result.stderr)
		self.assertEqual(self.edits(), [])

	def test_a_failed_edit_warns_and_still_exits_zero(self) -> None:
		self.given(["status/ready"])
		(self.bin / "edit-fails").write_text("", encoding="utf-8")
		result = self.run_claim()
		self.assertEqual(result.returncode, 0, result.stderr)
		self.assertIn("could not be recorded", result.stderr)


if __name__ == "__main__":
	unittest.main()
