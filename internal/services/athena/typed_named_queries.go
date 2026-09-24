package athena

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/protocol"
)

type createNamedQueryReq struct {
	Name               string `json:"Name"`
	Description        string `json:"Description"`
	Database           string `json:"Database"`
	QueryString        string `json:"QueryString"`
	ClientRequestToken string `json:"ClientRequestToken"`
	WorkGroup          string `json:"WorkGroup"`
}

type createNamedQueryResp struct {
	NamedQueryId string `json:"NamedQueryId"`
}

type namedQueryIDReq struct {
	NamedQueryId string `json:"NamedQueryId"`
}

type getNamedQueryResp struct {
	NamedQuery NamedQuery `json:"NamedQuery"`
}

type batchGetNamedQueryReq struct {
	NamedQueryIds []string `json:"NamedQueryIds"`
}

type batchGetNamedQueryResp struct {
	NamedQueries             []NamedQuery              `json:"NamedQueries"`
	UnprocessedNamedQueryIds []UnprocessedNamedQueryId `json:"UnprocessedNamedQueryIds"`
}

type listNamedQueriesReq struct {
	MaxResults int32  `json:"MaxResults"`
	NextToken  string `json:"NextToken"`
	WorkGroup  string `json:"WorkGroup"`
}

type listNamedQueriesResp struct {
	NamedQueryIds []string `json:"NamedQueryIds"`
	NextToken     string   `json:"NextToken,omitempty"`
}

type updateNamedQueryReq struct {
	NamedQueryId string  `json:"NamedQueryId"`
	Name         string  `json:"Name"`
	Description  *string `json:"Description"`
	QueryString  string  `json:"QueryString"`
}

func namedQueryLockKey(id string) string { return "named-query:" + id }

func (s *Service) createNamedQueryTyped(ctx context.Context, req *createNamedQueryReq) (*createNamedQueryResp, *protocol.AWSError) {
	if aerr := requireMembers([2]string{"Name", req.Name}, [2]string{"Database", req.Database}, [2]string{"QueryString", req.QueryString}); aerr != nil {
		return nil, aerr
	}
	fingerprinted := *req
	fingerprinted.ClientRequestToken = ""
	id, aerr := s.idempotent(ctx, "CreateNamedQuery", req.ClientRequestToken, &fingerprinted, func() (string, *protocol.AWSError) {
		wg, aerr := s.loadWorkGroup(ctx, orPrimary(req.WorkGroup))
		if aerr != nil {
			return "", aerr
		}
		nq := &NamedQuery{
			NamedQueryId: uuid.NewString(), Name: req.Name, Description: req.Description,
			Database: req.Database, QueryString: req.QueryString, WorkGroup: wg.Name,
		}
		if err := s.store.putNamedQuery(ctx, nq); err != nil {
			return "", errInternal(err)
		}
		return nq.NamedQueryId, nil
	})
	if aerr != nil {
		return nil, aerr
	}
	return &createNamedQueryResp{NamedQueryId: id}, nil
}

func (s *Service) requireNamedQuery(ctx context.Context, id string) (*NamedQuery, *protocol.AWSError) {
	if id == "" {
		return nil, errRequired("NamedQueryId")
	}
	nq, err := s.store.getNamedQuery(ctx, id)
	if err != nil {
		return nil, errInternal(err)
	}
	if nq == nil {
		return nil, errNamedQueryNotFound(id)
	}
	return nq, nil
}

func (s *Service) getNamedQueryTyped(ctx context.Context, req *namedQueryIDReq) (*getNamedQueryResp, *protocol.AWSError) {
	nq, aerr := s.requireNamedQuery(ctx, req.NamedQueryId)
	if aerr != nil {
		return nil, aerr
	}
	return &getNamedQueryResp{NamedQuery: *nq}, nil
}

func (s *Service) batchGetNamedQueryTyped(ctx context.Context, req *batchGetNamedQueryReq) (*batchGetNamedQueryResp, *protocol.AWSError) {
	if aerr := requireBatch("NamedQueryIds", len(req.NamedQueryIds)); aerr != nil {
		return nil, aerr
	}
	found, missed, aerr := batchGet(req.NamedQueryIds,
		func(id string) (*NamedQuery, *protocol.AWSError) { return s.requireNamedQuery(ctx, id) },
		func(id string, aerr *protocol.AWSError) UnprocessedNamedQueryId {
			return UnprocessedNamedQueryId{NamedQueryId: id, ErrorCode: batchErrorInvalidInput, ErrorMessage: aerr.Message}
		})
	if aerr != nil {
		return nil, aerr
	}
	return &batchGetNamedQueryResp{NamedQueries: found, UnprocessedNamedQueryIds: missed}, nil
}

// listNamedQueriesTyped lists a workgroup's saved queries. "If a workgroup
// is not specified, the saved queries for the primary workgroup are
// returned," per ListNamedQueries.
func (s *Service) listNamedQueriesTyped(ctx context.Context, req *listNamedQueriesReq) (*listNamedQueriesResp, *protocol.AWSError) {
	wg, aerr := s.loadWorkGroup(ctx, orPrimary(req.WorkGroup))
	if aerr != nil {
		return nil, aerr
	}
	all, err := scan[NamedQuery](ctx, s.store, nsNamedQueries, "")
	if err != nil {
		return nil, errInternal(err)
	}
	ids := make([]string, 0, len(all))
	for _, nq := range all {
		if nq.WorkGroup == wg.Name {
			ids = append(ids, nq.NamedQueryId)
		}
	}
	sort.Strings(ids)
	page, aerr := paginate(ids, req.MaxResults, req.NextToken, namedQueriesPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &listNamedQueriesResp{NamedQueryIds: page.Items, NextToken: page.NextToken}, nil
}

func (s *Service) updateNamedQueryTyped(ctx context.Context, req *updateNamedQueryReq) (*struct{}, *protocol.AWSError) {
	if aerr := requireMembers([2]string{"NamedQueryId", req.NamedQueryId}, [2]string{"Name", req.Name}, [2]string{"QueryString", req.QueryString}); aerr != nil {
		return nil, aerr
	}
	defer s.lock(namedQueryLockKey(req.NamedQueryId))()
	nq, aerr := s.requireNamedQuery(ctx, req.NamedQueryId)
	if aerr != nil {
		return nil, aerr
	}
	nq.Name, nq.QueryString = req.Name, req.QueryString
	if req.Description != nil {
		nq.Description = *req.Description
	}
	if err := s.store.putNamedQuery(ctx, nq); err != nil {
		return nil, errInternal(err)
	}
	return &struct{}{}, nil
}

func (s *Service) deleteNamedQueryTyped(ctx context.Context, req *namedQueryIDReq) (*struct{}, *protocol.AWSError) {
	defer s.lock(namedQueryLockKey(req.NamedQueryId))()
	if _, aerr := s.requireNamedQuery(ctx, req.NamedQueryId); aerr != nil {
		return nil, aerr
	}
	if err := s.store.delete(ctx, nsNamedQueries, req.NamedQueryId); err != nil {
		return nil, errInternal(err)
	}
	return &struct{}{}, nil
}
