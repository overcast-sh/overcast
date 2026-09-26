package glue

import (
	"context"
	"slices"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Partitions. A partition is named by its values, one per partition key of
// its table, in key order.

// Batch limits from the model's list lengths.
const (
	maxBatchCreatePartitions = 100
	maxBatchGetPartitions    = 1000
	maxBatchDeletePartitions = 25
	maxSegments              = 10
)

type createPartitionReq struct {
	catalogRef
	DatabaseName   string          `json:"DatabaseName" cbor:"DatabaseName"`
	TableName      string          `json:"TableName" cbor:"TableName"`
	PartitionInput *PartitionInput `json:"PartitionInput" cbor:"PartitionInput"`
}

type batchCreatePartitionReq struct {
	catalogRef
	DatabaseName       string           `json:"DatabaseName" cbor:"DatabaseName"`
	TableName          string           `json:"TableName" cbor:"TableName"`
	PartitionInputList []PartitionInput `json:"PartitionInputList" cbor:"PartitionInputList"`
}

type batchPartitionErrorsResp struct {
	Errors []PartitionError `json:"Errors" cbor:"Errors"`
}

type getPartitionReq struct {
	catalogRef
	DatabaseName    string   `json:"DatabaseName" cbor:"DatabaseName"`
	TableName       string   `json:"TableName" cbor:"TableName"`
	PartitionValues []string `json:"PartitionValues" cbor:"PartitionValues"`
}

type getPartitionResp struct {
	Partition *Partition `json:"Partition" cbor:"Partition"`
}

// segment is GetPartitions' Segment: split the table's partitions into
// TotalSegments disjoint sets and return set SegmentNumber.
type segment struct {
	SegmentNumber int `json:"SegmentNumber" cbor:"SegmentNumber"`
	TotalSegments int `json:"TotalSegments" cbor:"TotalSegments"`
}

type getPartitionsReq struct {
	catalogRef
	DatabaseName        string   `json:"DatabaseName" cbor:"DatabaseName"`
	TableName           string   `json:"TableName" cbor:"TableName"`
	Expression          string   `json:"Expression" cbor:"Expression"`
	ExcludeColumnSchema *bool    `json:"ExcludeColumnSchema" cbor:"ExcludeColumnSchema"`
	MaxResults          int32    `json:"MaxResults" cbor:"MaxResults"`
	NextToken           string   `json:"NextToken" cbor:"NextToken"`
	Segment             *segment `json:"Segment" cbor:"Segment"`
}

type getPartitionsResp struct {
	Partitions []*Partition `json:"Partitions" cbor:"Partitions"`
	NextToken  string       `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
}

type batchGetPartitionReq struct {
	catalogRef
	DatabaseName    string               `json:"DatabaseName" cbor:"DatabaseName"`
	TableName       string               `json:"TableName" cbor:"TableName"`
	PartitionsToGet []PartitionValueList `json:"PartitionsToGet" cbor:"PartitionsToGet"`
}

type batchGetPartitionResp struct {
	Partitions      []*Partition         `json:"Partitions" cbor:"Partitions"`
	UnprocessedKeys []PartitionValueList `json:"UnprocessedKeys" cbor:"UnprocessedKeys"`
}

type updatePartitionReq struct {
	catalogRef
	DatabaseName       string          `json:"DatabaseName" cbor:"DatabaseName"`
	TableName          string          `json:"TableName" cbor:"TableName"`
	PartitionValueList []string        `json:"PartitionValueList" cbor:"PartitionValueList"`
	PartitionInput     *PartitionInput `json:"PartitionInput" cbor:"PartitionInput"`
}

type deletePartitionReq struct {
	catalogRef
	DatabaseName    string   `json:"DatabaseName" cbor:"DatabaseName"`
	TableName       string   `json:"TableName" cbor:"TableName"`
	PartitionValues []string `json:"PartitionValues" cbor:"PartitionValues"`
}

type batchDeletePartitionReq struct {
	catalogRef
	DatabaseName       string               `json:"DatabaseName" cbor:"DatabaseName"`
	TableName          string               `json:"TableName" cbor:"TableName"`
	PartitionsToDelete []PartitionValueList `json:"PartitionsToDelete" cbor:"PartitionsToDelete"`
}

// checkPartitionValues requires one value per partition key.
func checkPartitionValues(t *tableRecord, values []string) *protocol.AWSError {
	if len(values) == 0 {
		return errInvalidInput("Partition values are required.")
	}
	if len(values) != len(t.PartitionKeys) {
		return errInvalidInput("The number of partition keys do not match the number of partition values.")
	}
	return nil
}

func partitionFromInput(in *PartitionInput, t *tableRecord) *Partition {
	return &Partition{
		Values:            in.Values,
		DatabaseName:      t.DatabaseName,
		TableName:         t.Name,
		LastAccessTime:    in.LastAccessTime,
		LastAnalyzedTime:  in.LastAnalyzedTime,
		StorageDescriptor: in.StorageDescriptor,
		Parameters:        in.Parameters,
		CatalogId:         t.CatalogId,
	}
}

// createOnePartition creates one partition of t. The caller holds lockTable.
func (s *Service) createOnePartition(ctx context.Context, t *tableRecord, in *PartitionInput) *protocol.AWSError {
	if aerr := checkPartitionValues(t, in.Values); aerr != nil {
		return aerr
	}
	_, found, err := s.store.getPartition(ctx, t.DatabaseName, t.Name, in.Values)
	if err != nil {
		return errInternal(err)
	}
	if found {
		return glueError(codeAlreadyExists, "Partition already exists.")
	}
	p := partitionFromInput(in, t)
	p.CreationTime = s.now()
	if err := s.store.putPartition(ctx, p); err != nil {
		return errInternal(err)
	}
	return nil
}

func (s *Service) createPartitionTyped(ctx context.Context, req *createPartitionReq) (*struct{}, *protocol.AWSError) {
	if req.PartitionInput == nil {
		return nil, errInvalidInput("PartitionInput is required.")
	}
	t, unlock, aerr := s.lockTable(ctx, req.DatabaseName, req.TableName)
	if aerr != nil {
		return nil, aerr
	}
	defer unlock()
	if aerr := s.createOnePartition(ctx, t, req.PartitionInput); aerr != nil {
		return nil, aerr
	}
	s.publishPartitionsChanged(ctx, t.DatabaseName, t.Name)
	return &struct{}{}, nil
}

func (s *Service) batchCreatePartitionTyped(ctx context.Context, req *batchCreatePartitionReq) (*batchPartitionErrorsResp, *protocol.AWSError) {
	if len(req.PartitionInputList) > maxBatchCreatePartitions {
		return nil, errInvalidInput("PartitionInputList must hold at most %d partitions.", maxBatchCreatePartitions)
	}
	t, unlock, aerr := s.lockTable(ctx, req.DatabaseName, req.TableName)
	if aerr != nil {
		return nil, aerr
	}
	defer unlock()
	resp := &batchPartitionErrorsResp{Errors: []PartitionError{}}
	for i := range req.PartitionInputList {
		in := &req.PartitionInputList[i]
		if aerr := s.createOnePartition(ctx, t, in); aerr != nil {
			resp.Errors = append(resp.Errors, PartitionError{PartitionValues: in.Values, ErrorDetail: errorDetail(aerr)})
		}
	}
	s.publishIfAnyApplied(ctx, t, len(req.PartitionInputList), resp)
	return resp, nil
}

func (s *Service) getPartitionTyped(ctx context.Context, req *getPartitionReq) (*getPartitionResp, *protocol.AWSError) {
	t, aerr := s.requireTable(ctx, normName(req.DatabaseName), normName(req.TableName))
	if aerr != nil {
		return nil, aerr
	}
	p, found, err := s.store.getPartition(ctx, t.DatabaseName, t.Name, req.PartitionValues)
	if err != nil {
		return nil, errInternal(err)
	}
	if !found {
		return nil, errPartitionNotFound()
	}
	return &getPartitionResp{Partition: p}, nil
}

func (s *Service) getPartitionsTyped(ctx context.Context, req *getPartitionsReq) (*getPartitionsResp, *protocol.AWSError) {
	if seg := req.Segment; seg != nil {
		if seg.TotalSegments < 1 || seg.TotalSegments > maxSegments || seg.SegmentNumber < 0 || seg.SegmentNumber >= seg.TotalSegments {
			return nil, errInvalidInput("Invalid Segment: SegmentNumber must be in [0, TotalSegments) and TotalSegments in [1, %d].", maxSegments)
		}
	}
	t, aerr := s.requireTable(ctx, normName(req.DatabaseName), normName(req.TableName))
	if aerr != nil {
		return nil, aerr
	}
	filter, err := parsePartitionExpression(req.Expression, t.PartitionKeys)
	if err != nil {
		return nil, errInvalidInput("Unsupported or invalid partition expression %q: %v", req.Expression, err)
	}
	all, err := s.store.listPartitions(ctx, t.DatabaseName, t.Name)
	if err != nil {
		return nil, errInternal(err)
	}
	excludeColumns := req.ExcludeColumnSchema != nil && *req.ExcludeColumnSchema
	matched := make([]*Partition, 0, len(all))
	for i, p := range all {
		if req.Segment != nil && i%req.Segment.TotalSegments != req.Segment.SegmentNumber {
			continue
		}
		if !filter.match(partitionValueMap(t.PartitionKeys, p.Values)) {
			continue
		}
		if excludeColumns {
			c := *p
			c.StorageDescriptor = p.StorageDescriptor.withoutColumns()
			p = &c
		}
		matched = append(matched, p)
	}
	page, aerr := paginate(matched, req.MaxResults, req.NextToken, partitionPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &getPartitionsResp{Partitions: page.Items, NextToken: page.NextToken}, nil
}

// partitionValueMap pairs each partition key (lowercased) with its value.
func partitionValueMap(keys []Column, values []string) map[string]string {
	m := make(map[string]string, len(keys))
	for i, k := range keys {
		if i < len(values) {
			m[partitionKeyName(k)] = values[i]
		}
	}
	return m
}

// batchGetPartitionTyped returns the requested partitions that exist. A
// partition that does not exist is simply absent from Partitions, as on AWS;
// UnprocessedKeys is for requests AWS could not get to, which never happens
// here.
func (s *Service) batchGetPartitionTyped(ctx context.Context, req *batchGetPartitionReq) (*batchGetPartitionResp, *protocol.AWSError) {
	if len(req.PartitionsToGet) > maxBatchGetPartitions {
		return nil, errInvalidInput("PartitionsToGet must hold at most %d partitions.", maxBatchGetPartitions)
	}
	t, aerr := s.requireTable(ctx, normName(req.DatabaseName), normName(req.TableName))
	if aerr != nil {
		return nil, aerr
	}
	resp := &batchGetPartitionResp{Partitions: []*Partition{}, UnprocessedKeys: []PartitionValueList{}}
	for _, key := range req.PartitionsToGet {
		p, found, err := s.store.getPartition(ctx, t.DatabaseName, t.Name, key.Values)
		if err != nil {
			return nil, errInternal(err)
		}
		if found {
			resp.Partitions = append(resp.Partitions, p)
		}
	}
	return resp, nil
}

// updatePartitionTyped replaces a partition's definition. PartitionInput's
// Values may differ from PartitionValueList, which moves the partition.
func (s *Service) updatePartitionTyped(ctx context.Context, req *updatePartitionReq) (*struct{}, *protocol.AWSError) {
	if req.PartitionInput == nil {
		return nil, errInvalidInput("PartitionInput is required.")
	}
	t, unlock, aerr := s.lockTable(ctx, req.DatabaseName, req.TableName)
	if aerr != nil {
		return nil, aerr
	}
	defer unlock()
	newValues := req.PartitionInput.Values
	if len(newValues) == 0 {
		newValues = req.PartitionValueList
	}
	if aerr := checkPartitionValues(t, newValues); aerr != nil {
		return nil, aerr
	}
	moved := !slices.Equal(newValues, req.PartitionValueList)
	cur, found, err := s.store.getPartition(ctx, t.DatabaseName, t.Name, req.PartitionValueList)
	if err != nil {
		return nil, errInternal(err)
	}
	if !found {
		return nil, errPartitionNotFound()
	}
	if moved {
		_, exists, err := s.store.getPartition(ctx, t.DatabaseName, t.Name, newValues)
		if err != nil {
			return nil, errInternal(err)
		}
		if exists {
			return nil, glueError(codeAlreadyExists, "Partition already exists.")
		}
	}
	in := *req.PartitionInput
	in.Values = newValues
	p := partitionFromInput(&in, t)
	p.CreationTime = cur.CreationTime
	if err := s.store.putPartition(ctx, p); err != nil {
		return nil, errInternal(err)
	}
	if moved {
		if err := s.store.deletePartition(ctx, t.DatabaseName, t.Name, req.PartitionValueList); err != nil {
			return nil, errInternal(err)
		}
	}
	s.publishPartitionsChanged(ctx, t.DatabaseName, t.Name)
	return &struct{}{}, nil
}

// deleteOnePartition deletes one partition of t. The caller holds lockTable.
func (s *Service) deleteOnePartition(ctx context.Context, t *tableRecord, values []string) *protocol.AWSError {
	_, found, err := s.store.getPartition(ctx, t.DatabaseName, t.Name, values)
	if err != nil {
		return errInternal(err)
	}
	if !found {
		return errPartitionNotFound()
	}
	if err := s.store.deletePartition(ctx, t.DatabaseName, t.Name, values); err != nil {
		return errInternal(err)
	}
	return nil
}

func (s *Service) deletePartitionTyped(ctx context.Context, req *deletePartitionReq) (*struct{}, *protocol.AWSError) {
	t, unlock, aerr := s.lockTable(ctx, req.DatabaseName, req.TableName)
	if aerr != nil {
		return nil, aerr
	}
	defer unlock()
	if aerr := s.deleteOnePartition(ctx, t, req.PartitionValues); aerr != nil {
		return nil, aerr
	}
	s.publishPartitionsChanged(ctx, t.DatabaseName, t.Name)
	return &struct{}{}, nil
}

func (s *Service) batchDeletePartitionTyped(ctx context.Context, req *batchDeletePartitionReq) (*batchPartitionErrorsResp, *protocol.AWSError) {
	if len(req.PartitionsToDelete) > maxBatchDeletePartitions {
		return nil, errInvalidInput("PartitionsToDelete must hold at most %d partitions.", maxBatchDeletePartitions)
	}
	t, unlock, aerr := s.lockTable(ctx, req.DatabaseName, req.TableName)
	if aerr != nil {
		return nil, aerr
	}
	defer unlock()
	resp := &batchPartitionErrorsResp{Errors: []PartitionError{}}
	for _, key := range req.PartitionsToDelete {
		if aerr := s.deleteOnePartition(ctx, t, key.Values); aerr != nil {
			resp.Errors = append(resp.Errors, PartitionError{PartitionValues: key.Values, ErrorDetail: errorDetail(aerr)})
		}
	}
	s.publishIfAnyApplied(ctx, t, len(req.PartitionsToDelete), resp)
	return resp, nil
}
