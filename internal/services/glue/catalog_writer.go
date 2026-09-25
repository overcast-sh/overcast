package glue

import (
	"context"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// CatalogWriter is the write half of the in-process catalog seam: Athena's
// DDL — CREATE DATABASE, CREATE EXTERNAL TABLE, ALTER TABLE ADD PARTITION,
// MSCK REPAIR TABLE, DROP — turns into these calls, as Athena's own DDL turns
// into Glue calls on AWS.
//
// Each method runs the Glue operation of the same name, so it validates,
// defaults and fails exactly as that operation does, and returns its
// *protocol.AWSError (AlreadyExistsException, EntityNotFoundException, …).
// The partition methods take any number of partitions and report each one
// they could not apply, as BatchCreatePartition and BatchDeletePartition do.
type CatalogWriter interface {
	CreateDatabase(ctx context.Context, in DatabaseInput) *protocol.AWSError
	DeleteDatabase(ctx context.Context, name string) *protocol.AWSError
	CreateTable(ctx context.Context, databaseName string, in TableInput) *protocol.AWSError
	DeleteTable(ctx context.Context, databaseName, tableName string) *protocol.AWSError
	CreatePartitions(ctx context.Context, databaseName, tableName string, in []PartitionInput) ([]PartitionError, *protocol.AWSError)
	DeletePartitions(ctx context.Context, databaseName, tableName string, values [][]string) ([]PartitionError, *protocol.AWSError)
}

// CatalogWriter returns the service's in-process catalog writer.
func (s *Service) CatalogWriter() CatalogWriter { return catalogWriter{s} }

type catalogWriter struct{ s *Service }

var _ CatalogWriter = catalogWriter{}

func (w catalogWriter) CreateDatabase(ctx context.Context, in DatabaseInput) *protocol.AWSError {
	_, aerr := w.s.createDatabaseTyped(ctx, &createDatabaseReq{DatabaseInput: &in})
	return aerr
}

func (w catalogWriter) DeleteDatabase(ctx context.Context, name string) *protocol.AWSError {
	_, aerr := w.s.deleteDatabaseTyped(ctx, &deleteDatabaseReq{Name: name})
	return aerr
}

func (w catalogWriter) CreateTable(ctx context.Context, databaseName string, in TableInput) *protocol.AWSError {
	_, aerr := w.s.createTableTyped(ctx, &createTableReq{DatabaseName: databaseName, TableInput: &in})
	return aerr
}

func (w catalogWriter) DeleteTable(ctx context.Context, databaseName, tableName string) *protocol.AWSError {
	_, aerr := w.s.deleteTableTyped(ctx, &deleteTableReq{DatabaseName: databaseName, Name: tableName})
	return aerr
}

func (w catalogWriter) CreatePartitions(ctx context.Context, databaseName, tableName string, in []PartitionInput) ([]PartitionError, *protocol.AWSError) {
	return inBatches(in, maxBatchCreatePartitions, func(batch []PartitionInput) (*batchPartitionErrorsResp, *protocol.AWSError) {
		return w.s.batchCreatePartitionTyped(ctx, &batchCreatePartitionReq{DatabaseName: databaseName, TableName: tableName, PartitionInputList: batch})
	})
}

func (w catalogWriter) DeletePartitions(ctx context.Context, databaseName, tableName string, values [][]string) ([]PartitionError, *protocol.AWSError) {
	keys := make([]PartitionValueList, len(values))
	for i, v := range values {
		keys[i] = PartitionValueList{Values: v}
	}
	return inBatches(keys, maxBatchDeletePartitions, func(batch []PartitionValueList) (*batchPartitionErrorsResp, *protocol.AWSError) {
		return w.s.batchDeletePartitionTyped(ctx, &batchDeletePartitionReq{DatabaseName: databaseName, TableName: tableName, PartitionsToDelete: batch})
	})
}

// inBatches runs a batch operation over items in batches of at most size,
// collecting each batch's per-item errors. A failure of a whole batch stops it.
func inBatches[T any](items []T, size int, run func([]T) (*batchPartitionErrorsResp, *protocol.AWSError)) ([]PartitionError, *protocol.AWSError) {
	var errs []PartitionError
	for start := 0; start < len(items); start += size {
		resp, aerr := run(items[start:min(start+size, len(items))])
		if aerr != nil {
			return errs, aerr
		}
		errs = append(errs, resp.Errors...)
	}
	return errs, nil
}
