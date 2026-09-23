#!/usr/bin/env bash
# Claim an issue before working it, and check nobody else already has.
#
# Two agents picked up issue #1325 on 2026-08-22 and wrote two different fixes
# for the same window. The second one (PR #1351) was opened with auto-merge
# armed 1h43m after the first (PR #1331) had already been cross-referenced onto
# the issue, and 25 minutes after that first PR had *merged*. Nothing caught it
# until GitHub reported the branch as conflicting.
#
# Two things went wrong, and only one of them is a missing convention:
#
#   - status/in-progress already existed in the label taxonomy ("Actively being
#     worked by an agent or maintainer") and neither agent set it. A convention
#     nothing enforces is not a convention.
#   - The signal that would have caught it needed no convention at all. GitHub
#     cross-references a PR onto an issue the moment the PR body names it, so
#     PR #1331 was on #1325's timeline from the minute it opened. `gh issue
#     view` renders title, body and comments — not timeline events — so looking
#     at the issue the obvious way showed an open, unassigned, uncommented
#     issue with no hint that anyone was on it.
#
# So this reads the timeline, and it separates the two kinds of reference the
# timeline holds. A PR that declares it *closes* the issue is claiming the whole
# thing; a PR that merely mentions it is often contributing a piece, or citing
# it for context, and must not be reported as a collision. GitHub knows the
# difference — closingIssuesReferences is populated only by a closing keyword —
# so this asks it rather than guessing from prose.
#
# Usage:
#   scripts/issue-claim.sh              # issue number from the branch name
#   scripts/issue-claim.sh 1325         # or given explicitly
#   scripts/issue-claim.sh --check      # report only, change nothing
#
# Without --check, a clear issue is claimed: status/in-progress replaces any of
# status/ready, status/needs-triage or status/blocked in one edit, plus
# self-assign. Every agent here pushes as the same GitHub user, so the assignee
# says "someone is on this" and not who; the label is the part that carries
# meaning.
#
# Exit codes:
#   0  clear, or claimed, or nothing to check, or unable to check
#   3  a live claim by someone else, or the issue is already closed
#
# Exit 0 on a missing gh, a missing jq, no auth and no network is deliberate.
# This is a warning tool wired into a push hook: it must never be the reason a
# push fails, exactly as scripts/verify-changed.sh stands aside when a toolchain
# is unavailable. Being unable to check is not evidence of being clear, and it
# says so out loud rather than passing quietly.
#
# See AGENTS.md § Claiming an issue.
set -euo pipefail

mode=claim
issue=""

usage() {
	sed -n '3,42p' "$0" | sed 's/^# \{0,1\}//'
}

for arg in "$@"; do
	case "$arg" in
	--check) mode=check ;;
	-h | --help)
		usage
		exit 0
		;;
	'#'[0-9]* | [0-9]*) issue="${arg#\#}" ;;
	*)
		printf 'issue-claim: unrecognised argument %s\n' "$arg" >&2
		exit 2
		;;
	esac
done

note() { printf 'issue-claim: %s\n' "$*" >&2; }

# The branch name is the one place the issue number is already written down, and
# it is written down by convention on every agent branch: claude/issue-<n>-*.
# A branch that does not name an issue has nothing to check, which is a silent
# exit rather than a complaint — most branches are not issue branches.
if [ -z "$issue" ]; then
	branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || true)
	case "$branch" in
	*issue-[0-9]*)
		issue=$(printf '%s' "$branch" | sed -n 's/.*issue-\([0-9][0-9]*\).*/\1/p')
		;;
	esac
fi
[ -n "$issue" ] || exit 0

for tool in gh jq; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		note "cannot check #$issue for duplicate work: $tool is not installed"
		exit 0
	fi
done
if ! gh auth status >/dev/null 2>&1; then
	note "cannot check #$issue for duplicate work: gh is not authenticated"
	exit 0
fi

nwo=$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null || true)
if [ -z "$nwo" ]; then
	note "cannot check #$issue for duplicate work: no GitHub remote reachable"
	exit 0
fi
owner=${nwo%%/*}
repo=${nwo##*/}
branch=${branch:-$(git rev-parse --abbrev-ref HEAD 2>/dev/null || true)}

# One round trip answers all of it: whether the issue is still open, every pull
# request that has ever referenced it, and — per PR — whether that reference is
# a closing one. Asking per-PR afterwards would be a request per result for a
# field the timeline can carry itself.
query=$(
	cat <<'GRAPHQL'
query($owner:String!,$repo:String!,$num:Int!){
  repository(owner:$owner,name:$repo){
    issue(number:$num){
      state
      title
      labels(first:100){nodes{name}}
      timelineItems(last:100, itemTypes:[CROSS_REFERENCED_EVENT]){
        nodes{
          ... on CrossReferencedEvent{
            source{
              ... on PullRequest{
                number title state isDraft url headRefName
                closingIssuesReferences(first:20){nodes{number}}
              }
            }
          }
        }
      }
    }
  }
}
GRAPHQL
)

if ! response=$(gh api graphql -F owner="$owner" -F repo="$repo" -F num="$issue" -f query="$query" 2>/dev/null); then
	note "cannot check #$issue for duplicate work: the GitHub API call failed"
	exit 0
fi

state=$(printf '%s' "$response" | jq -r '.data.repository.issue.state // empty')
if [ -z "$state" ]; then
	note "cannot check #$issue for duplicate work: no such issue in $nwo"
	exit 0
fi
title=$(printf '%s' "$response" | jq -r '.data.repository.issue.title // empty')

# A PR that says it closes this issue and has not itself been closed is someone
# else doing this work — open means in flight, merged means already done. A PR
# on *this* branch is us. Everything else that merely names the issue is
# reported separately and decides nothing: contributing to an issue is not
# claiming it.
claims=$(printf '%s' "$response" | jq -r --argjson num "$issue" --arg branch "$branch" '
  [ .data.repository.issue.timelineItems.nodes[]?.source
    | select(.number != null)
    | select([.closingIssuesReferences.nodes[]?.number] | index($num))
    | select(.state != "CLOSED")
    | select(.headRefName != $branch)
  ] | .[] | "  #\(.number) [\(.state)\(if .isDraft then " DRAFT" else "" end)] \(.title)\n    \(.url)  (branch: \(.headRefName))"
')
mentions=$(printf '%s' "$response" | jq -r --argjson num "$issue" --arg branch "$branch" '
  [ .data.repository.issue.timelineItems.nodes[]?.source
    | select(.number != null)
    | select([.closingIssuesReferences.nodes[]?.number] | index($num) | not)
    | select(.headRefName != $branch)
  ] | .[] | "  #\(.number) [\(.state)] \(.title)"
')

conflict=0
if [ "$state" = "CLOSED" ]; then
	note "#$issue is already CLOSED — \"$title\""
	conflict=1
fi
if [ -n "$claims" ]; then
	note "#$issue is already claimed by a pull request that declares it closes it:"
	printf '%s\n' "$claims" >&2
	conflict=1
fi
if [ -n "$mentions" ]; then
	note "#$issue is also referenced (not claimed) by:"
	printf '%s\n' "$mentions" >&2
	note "those reference it without a closing keyword — usually a contribution or a citation, not a collision"
fi

if [ "$conflict" = 1 ]; then
	note "read those before writing anything. If your change is genuinely distinct, say so in your PR body and carry on."
	exit 3
fi

if [ "$mode" = check ]; then
	note "#$issue is unclaimed — \"$title\""
	exit 0
fi

# Lifecycle labels are a state machine (github-issue-lifecycle skill § Updating
# Lifecycle State): claiming moves the issue out of whichever pre-work state it
# was in, in the same edit. Adding status/in-progress alone left #2062, #2063,
# #2064, #2067 and #2088 carrying status/ready too, so they still showed up in
# the ready queue agents pick work from (#2094). gh fails the whole edit when
# asked to remove a label the issue does not have, so only the ones present are
# passed. The array is never empty, which keeps "${edit[@]}" safe under set -u
# on macOS's bash 3.2.
edit=(--add-label status/in-progress --add-assignee @me)
removed=""
for label in status/ready status/needs-triage status/blocked; do
	if printf '%s' "$response" | jq -e --arg l "$label" '[.data.repository.issue.labels.nodes[]?.name] | index($l)' >/dev/null; then
		edit+=(--remove-label "$label")
		removed="$removed, removed $label"
	fi
done

if gh issue edit "$issue" "${edit[@]}" >/dev/null 2>&1; then
	note "claimed #$issue (status/in-progress, assigned$removed) — \"$title\""
else
	# A claim that cannot be recorded is not worth failing over: the check above
	# is the part that prevents duplicated work, and it has already passed.
	note "#$issue is unclaimed, but the claim could not be recorded (label or assignee edit failed)"
fi
exit 0
