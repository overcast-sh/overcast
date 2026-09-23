package glue

import (
	"context"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

var glueTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    codeInvalidInput,
	InvalidCode:     codeInvalidInput,
	ExceededMessage: "Too many tags.",
}

type glueTagResourceReq struct {
	ResourceArn string            `json:"ResourceArn" cbor:"ResourceArn"`
	TagsToAdd   map[string]string `json:"TagsToAdd" cbor:"TagsToAdd"`
}

type glueUntagResourceReq struct {
	ResourceArn  string   `json:"ResourceArn" cbor:"ResourceArn"`
	TagsToRemove []string `json:"TagsToRemove" cbor:"TagsToRemove"`
}

type glueListTagsForResourceReq struct {
	ResourceArn string `json:"ResourceArn" cbor:"ResourceArn"`
}

type glueListTagsForResourceResp struct {
	Tags map[string]string `json:"Tags" cbor:"Tags"`
}

// glueARNToDBAndTable splits arn:aws:glue:<region>:<account>:database/<db>
// and .../table/<db>/<table> into their (folded) names.
func glueARNToDBAndTable(arn string) (dbName, tableName string) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		return "", ""
	}
	rType, rest, ok := strings.Cut(parts[5], "/")
	if !ok {
		return "", ""
	}
	switch rType {
	case "database":
		return normName(rest), ""
	case "table":
		if db, table, ok := strings.Cut(rest, "/"); ok {
			return normName(db), normName(table)
		}
	}
	return "", ""
}

// tagTarget resolves a tag operation's ARN to the record that carries the
// tags, how to save it, and the lock guarding it.
type tagTarget struct {
	lockKey string
	load    func(ctx context.Context, key string) (serviceutil.Taggable, *protocol.AWSError)
	save    func(ctx context.Context, rec serviceutil.Taggable) *protocol.AWSError
}

func (s *Service) tagTarget(arn string) (*tagTarget, *protocol.AWSError) {
	dbName, tableName := glueARNToDBAndTable(arn)
	switch {
	case tableName != "":
		return &tagTarget{
			lockKey: "table:" + tableKey(dbName, tableName),
			load: func(ctx context.Context, _ string) (serviceutil.Taggable, *protocol.AWSError) {
				return s.requireTable(ctx, dbName, tableName)
			},
			save: func(ctx context.Context, rec serviceutil.Taggable) *protocol.AWSError {
				if err := s.store.putTable(ctx, rec.(*tableRecord)); err != nil {
					return errInternal(err)
				}
				return nil
			},
		}, nil
	case dbName != "":
		return &tagTarget{
			lockKey: "db:" + dbName,
			load: func(ctx context.Context, _ string) (serviceutil.Taggable, *protocol.AWSError) {
				return s.requireDatabase(ctx, dbName)
			},
			save: func(ctx context.Context, rec serviceutil.Taggable) *protocol.AWSError {
				if err := s.store.putDatabase(ctx, rec.(*databaseRecord)); err != nil {
					return errInternal(err)
				}
				return nil
			},
		}, nil
	default:
		return nil, glueError(codeEntityNotFound, "Resource not found.")
	}
}

func (s *Service) tagResourceTyped(ctx context.Context, req *glueTagResourceReq) (*struct{}, *protocol.AWSError) {
	target, aerr := s.tagTarget(req.ResourceArn)
	if aerr != nil {
		return nil, aerr
	}
	defer s.locks.Lock(target.lockKey)()
	if aerr := serviceutil.ApplyInlineTags(ctx, target.lockKey, req.TagsToAdd, glueTagCfg, target.load, target.save); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (s *Service) untagResourceTyped(ctx context.Context, req *glueUntagResourceReq) (*struct{}, *protocol.AWSError) {
	target, aerr := s.tagTarget(req.ResourceArn)
	if aerr != nil {
		return nil, aerr
	}
	defer s.locks.Lock(target.lockKey)()
	if aerr := serviceutil.RemoveInlineTags(ctx, target.lockKey, req.TagsToRemove, target.load, target.save); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (s *Service) listTagsForResourceTyped(ctx context.Context, req *glueListTagsForResourceReq) (*glueListTagsForResourceResp, *protocol.AWSError) {
	target, aerr := s.tagTarget(req.ResourceArn)
	if aerr != nil {
		return nil, aerr
	}
	tags, aerr := serviceutil.ListInlineTags(ctx, target.lockKey, target.load)
	if aerr != nil {
		return nil, aerr
	}
	return &glueListTagsForResourceResp{Tags: tags}, nil
}
