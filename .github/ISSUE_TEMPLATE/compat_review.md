---
name: Compatibility review
about: Review an AWS service operation or resource for Overcast compatibility
title: 'Compat review: <service> <operation/resource>'
labels: compat, aws-fidelity, area/emulation, status/ready
assignees: ''
---

## Scope

<!-- Which service operation, resource, or behavior needs compatibility review? -->

**Service:**
**Operation/resource:**
**Current docs/status:** <!-- e.g. docs/services/sqs.md, STATUS.md, compatibility tracker -->

<!-- Links this issue from the public compatibility report (overcast.sh/compat).
     Replace the target: <service>[/<group-or-operation>[/<test>]][@<suite>],
     comma-separated for several. Delete the line if no compat result applies. -->
<!-- compat:<service>/<operation> -->

## References

<!-- Add AWS docs first. Add Overcast files/tests if known. -->

- AWS API docs:
- AWS developer guide:
- Current implementation:
- Existing tests:

## Review Checklist

- [ ] Compare protocol, target header, path, query/body encoding, and content type
- [ ] Compare required and optional parameters
- [ ] Compare validation rules and boundary values
- [ ] Compare success status code and response shape
- [ ] Compare empty-result behavior
- [ ] Compare error codes, messages where stable, and HTTP status codes
- [ ] Compare pagination, idempotency, ordering, and state transitions if applicable
- [ ] Add or update tests for behavior Overcast claims to support
- [ ] Fix implementation drift or document intentional partial support
- [ ] Update docs/status if support claims or caveats changed

## Scenario Matrix

<!-- List documented scenarios that must be reviewed. -->

- [ ] Default/minimal resource configuration:
- [ ] Common production-style configuration:
- [ ] Duplicate, missing, malformed, or deleted resource cases:
- [ ] Boundary values:
- [ ] Cross-resource relationships:

## Findings

<!-- Fill during review. Include evidence links and exact observed behavior. -->

## Completion Criteria

- [ ] AWS docs are linked and reviewed
- [ ] Tests cover supported behavior
- [ ] Implementation matches documented behavior or gaps are documented
- [ ] Service docs/status are updated if behavior or support level changed
- [ ] Verification commands are recorded in a comment or PR

<!-- Suggested labels: service/<name>, needs-tests, needs-aws-verification, implementation-mismatch, intentionally-partial, priority/p2 -->
