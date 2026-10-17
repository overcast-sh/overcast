//go:build dev

package main

import (
	"fmt"
	"strings"
)

// Pagination: the model-dependent half of a list clause's `paginate`.
//
// The IR carries the three member names (see paginate in ir.go) so that no
// interpreter needs the model. Where they come from is the operation's Smithy
// @paginated trait in the pinned snapshot, and nowhere else: a token name is
// never guessed from a naming convention, because `NextToken`, `nextToken`,
// `Marker` and `NextMarker` are all real and a wrong one fails as a service
// that "never paginates".

// paginationMembers is what a @paginated operation has to say for a clause to
// walk it.
type paginationMembers struct {
	InputToken, OutputToken, PageSizeMember string
}

// resolvePagination reads op's @paginated trait and returns the members a
// paginate clause needs, or the refusal naming what is missing.
//
// All of these refuse rather than error because the same question is asked of
// a recipe's clause, where declining is the generator's to do and the gap is
// recorded; an authored scenario is held to the same answers as an error by
// checkAuthoredPagination.
func resolvePagination(model *serviceModel, op string) (paginationMembers, *refusal) {
	if !model.IsPaginated(op) {
		return paginationMembers{}, refuse(reasonNotPaginated,
			fmt.Sprintf("%s declares no @paginated trait, so the model does not say which members carry the page size and the tokens, and they are never guessed", op))
	}
	trait := model.Pagination(op)
	for _, field := range []struct{ name, value string }{
		{"inputToken", trait.InputToken},
		{"outputToken", trait.OutputToken},
		{"pageSize", trait.PageSize},
	} {
		if field.value == "" {
			return paginationMembers{}, refuse(reasonPaginatedIncomplete+":"+field.name,
				fmt.Sprintf("%s's @paginated trait names no %s, so a walk cannot be built from it", op, field.name))
		}
	}
	members := paginationMembers{InputToken: trait.InputToken, OutputToken: trait.OutputToken, PageSizeMember: trait.PageSize}

	input, output := model.InputShape(op), model.OutputShape(op)
	for _, member := range []struct{ field, name, shape, want string }{
		{"inputToken", members.InputToken, input, "string"},
		{"outputToken", members.OutputToken, output, "string"},
		{"pageSize", members.PageSizeMember, input, "integer"},
	} {
		if strings.ContainsAny(member.name, ".[]") {
			return paginationMembers{}, refuse(reasonUnsupportedPageToken+":"+member.field,
				fmt.Sprintf("%s's @paginated %s is the path %q; only a top-level member can be walked", op, member.field, member.name))
		}
		target, ok := model.MemberTarget(member.shape, member.name)
		if !ok {
			return paginationMembers{}, refuse(reasonUnsupportedPageToken+":"+member.field,
				fmt.Sprintf("%s's @paginated %s is %q, which is not a member of %s", op, member.field, member.name, bareShapeName(member.shape)))
		}
		if kind := model.Kind(target); kind != member.want {
			return paginationMembers{}, refuse(reasonUnsupportedPageToken+":"+member.field,
				fmt.Sprintf("%s's @paginated %s %q is a %s, and a walk needs a %s", op, member.field, member.name, kind, member.want))
		}
	}
	return members, nil
}

// completePagination fills a recipe clause's paginate from the trait. A recipe
// writes only the page size and the floor; naming the members as well would be
// a second copy of the trait to keep in step with it.
func completePagination(model *serviceModel, a *assertion) (*refusal, error) {
	if a.Paginate == nil {
		return nil, nil
	}
	if a.Paginate.InputToken != "" || a.Paginate.OutputToken != "" || a.Paginate.PageSizeMember != "" {
		return nil, fmt.Errorf("a recipe's paginate gives pageSize and minPages only; inputToken, outputToken and pageSizeMember come from the operation's @paginated trait")
	}
	members, ref := resolvePagination(model, a.Call.Op)
	if ref != nil {
		return ref, nil
	}
	a.Paginate.InputToken = members.InputToken
	a.Paginate.OutputToken = members.OutputToken
	a.Paginate.PageSizeMember = members.PageSizeMember
	if _, set := a.Call.Params[members.PageSizeMember]; set {
		return nil, fmt.Errorf("paginate sends %s itself; the call's params must not set it", members.PageSizeMember)
	}
	if _, set := a.Call.Params[members.InputToken]; set {
		return nil, fmt.Errorf("paginate sends %s itself; the call's params must not set it", members.InputToken)
	}
	return nil, nil
}

// checkAuthoredPagination holds an authored clause's paginate to the trait.
// The file is an input the generator never rewrites, so it carries the three
// members itself; this is what stops them drifting from the model. Wrong here
// is an error rather than a refusal, as for every authored check.
func checkAuthoredPagination(model *serviceModel, a assertion) error {
	if a.Paginate == nil {
		return nil
	}
	op := a.Call.Op
	members, ref := resolvePagination(model, op)
	if ref != nil {
		return fmt.Errorf("paginate on %s: %s: %s", op, ref.Reason, ref.Detail)
	}
	want := paginationMembers{InputToken: a.Paginate.InputToken, OutputToken: a.Paginate.OutputToken, PageSizeMember: a.Paginate.PageSizeMember}
	if want != members {
		return fmt.Errorf("paginate on %s names inputToken %q, outputToken %q, pageSizeMember %q; the @paginated trait says %q, %q, %q",
			op, want.InputToken, want.OutputToken, want.PageSizeMember, members.InputToken, members.OutputToken, members.PageSizeMember)
	}
	return nil
}
