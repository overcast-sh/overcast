package iam

import (
	"context"
	"net/http"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// AWS refuses to delete an IAM entity that still has dependencies, with
// DeleteConflict (HTTP 409) and a message naming what the caller has to clear
// first. The strings below are AWS's own, reproduced verbatim — tooling
// matches on them (Terraform, aws-nuke, eksctl and CDK all key retry and
// cleanup behaviour off the message, not just the code).
//
//   - "Cannot delete entity, must delete access keys first."
//     "Cannot delete entity, must delete login profile first."
//     https://github.com/hashicorp/terraform/issues/8621
//   - "Cannot delete entity, must remove users from group first."
//     https://github.com/hashicorp/terraform/issues/3774 (DeleteUser),
//     https://github.com/hashicorp/terraform/issues/29147 (DeleteGroup)
//   - "Cannot delete entity, must detach all policies first."
//     https://github.com/hashicorp/terraform-provider-aws/issues/40059
//   - "Cannot delete entity, must remove roles from instance profile first."
//     https://github.com/hashicorp/terraform-provider-aws/issues/18534
//   - "Cannot delete a policy attached to entities."
//     https://github.com/hashicorp/terraform-provider-aws/issues/32906
//
// "Cannot delete entity, must delete policies first." is the inline-policy
// counterpart of the detach message and is what AWS returns for a user or role
// that still has a PutUserPolicy/PutRolePolicy document.
const (
	msgDeleteAccessKeysFirst  = "Cannot delete entity, must delete access keys first."
	msgDeleteInlinePolicies   = "Cannot delete entity, must delete policies first."
	msgDetachManagedPolicies  = "Cannot delete entity, must detach all policies first."
	msgRemoveFromGroupFirst   = "Cannot delete entity, must remove users from group first."
	msgRemoveFromProfileFirst = "Cannot delete entity, must remove roles from instance profile first."
	msgPolicyAttachedToEntity = "Cannot delete a policy attached to entities."
)

// errDeleteConflict is AWS's DeleteConflict: the entity has dependencies that
// must be removed before it can be deleted.
func errDeleteConflict(message string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "DeleteConflict",
		Message:    message,
		HTTPStatus: http.StatusConflict,
	}
}

// dependency is one reason a delete is refused: a predicate over the entity's
// stored state, and the AWS message to return when it holds.
type dependency struct {
	blocked bool
	message string
}

// firstBlocker returns the DeleteConflict for the first dependency that holds,
// or nil when the entity is free to delete.
//
// The order the checks are listed in is the order AWS's own API Reference lists
// the prerequisites for each delete, so a caller clearing them top to bottom
// clears them in the order the errors arrive. AWS does not document which
// dependency wins when several block at once; the list order is Overcast's
// stable choice, and every message a caller can receive is reachable by
// clearing the ones above it.
func firstBlocker(checks ...dependency) *protocol.AWSError {
	for _, c := range checks {
		if c.blocked {
			return errDeleteConflict(c.message)
		}
	}
	return nil
}

// checkUserDeletable reports AWS's DeleteConflict when a user still has
// dependencies. Ordered as the DeleteUser API Reference lists them: credentials
// first, then inline policies, then attached managed policies, then group
// membership.
//
// Login profiles, signing certificates, SSH keys, Git credentials and MFA
// devices are also on AWS's list; Overcast models none of them (there is no
// CreateLoginProfile, and so on), so no stored user can ever carry one and a
// check for them would be unreachable. AWS's wording for the first of them is
// recorded in the message block above for whoever adds it.
func (h *Handler) checkUserDeletable(ctx context.Context, u *User) *protocol.AWSError {
	groups, aerr := h.store.listGroupsForUser(ctx, u.UserName)
	if aerr != nil {
		return aerr
	}
	return firstBlocker(
		dependency{len(u.AccessKeys) > 0, msgDeleteAccessKeysFirst},
		dependency{len(u.InlinePolicies) > 0, msgDeleteInlinePolicies},
		dependency{len(u.AttachedPolicies) > 0, msgDetachManagedPolicies},
		dependency{len(groups) > 0, msgRemoveFromGroupFirst},
	)
}

// checkRoleDeletable reports AWS's DeleteConflict when a role still has
// dependencies. The instance-profile association is checked first because it is
// the one dependency that lives outside the role's own record, and it is the
// one AWS's DeleteRole guidance leads with.
func (h *Handler) checkRoleDeletable(ctx context.Context, r *Role) *protocol.AWSError {
	profiles, aerr := h.store.listProfilesForRole(ctx, r.RoleName)
	if aerr != nil {
		return aerr
	}
	return firstBlocker(
		dependency{len(profiles) > 0, msgRemoveFromProfileFirst},
		dependency{len(r.InlinePolicies) > 0, msgDeleteInlinePolicies},
		dependency{len(r.AttachedPolicies) > 0, msgDetachManagedPolicies},
	)
}

// checkInstanceProfileDeletable reports AWS's DeleteConflict when an instance
// profile still holds a role: "Deletes the specified instance profile. The
// instance profile must not have an associated role."
// https://docs.aws.amazon.com/IAM/latest/APIReference/API_DeleteInstanceProfile.html
//
// AWS's message is the same one DeleteRole gives for the other end of the same
// association — what it names is the membership, not which side asked.
func (h *Handler) checkInstanceProfileDeletable(_ context.Context, p *InstanceProfile) *protocol.AWSError {
	return firstBlocker(dependency{len(p.Roles) > 0, msgRemoveFromProfileFirst})
}

// checkGroupDeletable reports AWS's DeleteConflict when a group still has
// members or policies.
func (h *Handler) checkGroupDeletable(_ context.Context, g *Group) *protocol.AWSError {
	return firstBlocker(
		dependency{len(g.Members) > 0, msgRemoveFromGroupFirst},
		dependency{len(g.InlinePolicies) > 0, msgDeleteInlinePolicies},
		dependency{len(g.AttachedPolicies) > 0, msgDetachManagedPolicies},
	)
}

// checkPolicyDeletable reports AWS's DeleteConflict when a managed policy is
// still attached to any user, role or group. AWS requires every attachment to
// be removed (DetachUserPolicy / DetachRolePolicy / DetachGroupPolicy) before
// the policy itself can go.
func (h *Handler) checkPolicyDeletable(ctx context.Context, p *Policy) *protocol.AWSError {
	attached, aerr := h.store.policyIsAttached(ctx, p.Arn)
	if aerr != nil {
		return aerr
	}
	return firstBlocker(dependency{attached, msgPolicyAttachedToEntity})
}
