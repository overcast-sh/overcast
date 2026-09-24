package athena

import (
	"context"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

type tagResourceReq struct {
	ResourceARN string                `json:"ResourceARN"`
	Tags        []serviceutil.TagPair `json:"Tags"`
}

type untagResourceReq struct {
	ResourceARN string   `json:"ResourceARN"`
	TagKeys     []string `json:"TagKeys"`
}

type listTagsForResourceReq struct {
	ResourceARN string `json:"ResourceARN"`
	MaxResults  int32  `json:"MaxResults"`
	NextToken   string `json:"NextToken"`
}

type listTagsForResourceResp struct {
	Tags      []serviceutil.TagPair `json:"Tags"`
	NextToken string                `json:"NextToken,omitempty"`
}

// tagTarget is the record a tag operation's ARN names, how to load and save
// it, and the lock guarding it.
type tagTarget struct {
	lockKey string
	load    func(ctx context.Context, key string) (serviceutil.Taggable, *protocol.AWSError)
	save    func(ctx context.Context, rec serviceutil.Taggable) *protocol.AWSError
}

// tagsPageSize is ListTagsForResource's page cap (MaxTagsCount).
const tagsPageSize = 75

// tagTarget resolves arn:aws:athena:<region>:<account>:workgroup/<name> and
// .../datacatalog/<name>, the two taggable resources Overcast emulates.
func (s *Service) tagTarget(arn string) (*tagTarget, *protocol.AWSError) {
	if arn == "" {
		return nil, errRequired("ResourceARN")
	}
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		return nil, errInvalidRequest("Invalid ResourceARN format")
	}
	if parts[4] != s.cfg.AccountID {
		return nil, athenaError(codeResourceNotFound, "Resource %s was not found.", arn)
	}
	kind, name, _ := strings.Cut(parts[5], "/")
	switch kind {
	case "workgroup":
		return &tagTarget{
			lockKey: workGroupLockKey(name),
			load: func(ctx context.Context, _ string) (serviceutil.Taggable, *protocol.AWSError) {
				return s.loadWorkGroup(ctx, name)
			},
			save: func(ctx context.Context, rec serviceutil.Taggable) *protocol.AWSError {
				return internalOrNil(s.store.putWorkGroup(ctx, rec.(*workGroupRecord)))
			},
		}, nil
	case "datacatalog":
		return &tagTarget{
			lockKey: dataCatalogLockKey(name),
			load: func(ctx context.Context, _ string) (serviceutil.Taggable, *protocol.AWSError) {
				return s.requireCustomCatalog(ctx, name)
			},
			save: func(ctx context.Context, rec serviceutil.Taggable) *protocol.AWSError {
				return internalOrNil(s.store.putDataCatalog(ctx, rec.(*dataCatalogRecord)))
			},
		}, nil
	default:
		return nil, errInvalidRequest("ResourceARN must name a workgroup or a data catalog")
	}
}

func (s *Service) tagResourceTyped(ctx context.Context, req *tagResourceReq) (*struct{}, *protocol.AWSError) {
	target, aerr := s.tagTarget(req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	defer s.lock(target.lockKey)()
	if aerr := serviceutil.ApplyInlineTags(ctx, target.lockKey, serviceutil.TagsFromList(req.Tags), athenaTagCfg, target.load, target.save); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (s *Service) untagResourceTyped(ctx context.Context, req *untagResourceReq) (*struct{}, *protocol.AWSError) {
	target, aerr := s.tagTarget(req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	defer s.lock(target.lockKey)()
	if aerr := serviceutil.RemoveInlineTags(ctx, target.lockKey, req.TagKeys, target.load, target.save); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (s *Service) listTagsForResourceTyped(ctx context.Context, req *listTagsForResourceReq) (*listTagsForResourceResp, *protocol.AWSError) {
	target, aerr := s.tagTarget(req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	tags, aerr := serviceutil.ListInlineTags(ctx, target.lockKey, target.load)
	if aerr != nil {
		return nil, aerr
	}
	page, aerr := paginate(serviceutil.TagsToList(tags), req.MaxResults, req.NextToken, tagsPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &listTagsForResourceResp{Tags: page.Items, NextToken: page.NextToken}, nil
}
