package glue

import (
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
)

func (s *Service) typedOps() map[string]op.Operation {
	return map[string]op.Operation{
		// Databases
		"CreateDatabase": op.NewTyped[createDatabaseReq, struct{}]("CreateDatabase", s.createDatabaseTyped),
		"GetDatabase":    op.NewTyped[getDatabaseReq, getDatabaseResp]("GetDatabase", s.getDatabaseTyped),
		"GetDatabases":   op.NewTyped[getDatabasesReq, getDatabasesResp]("GetDatabases", s.getDatabasesTyped),
		"UpdateDatabase": op.NewTyped[updateDatabaseReq, struct{}]("UpdateDatabase", s.updateDatabaseTyped),
		"DeleteDatabase": op.NewTyped[deleteDatabaseReq, struct{}]("DeleteDatabase", s.deleteDatabaseTyped),
		// Tables
		"CreateTable":      op.NewTyped[createTableReq, createTableResp]("CreateTable", s.createTableTyped),
		"GetTable":         op.NewTyped[getTableReq, getTableResp]("GetTable", s.getTableTyped),
		"GetTables":        op.NewTyped[getTablesReq, getTablesResp]("GetTables", s.getTablesTyped),
		"UpdateTable":      op.NewTyped[updateTableReq, struct{}]("UpdateTable", s.updateTableTyped),
		"DeleteTable":      op.NewTyped[deleteTableReq, struct{}]("DeleteTable", s.deleteTableTyped),
		"BatchDeleteTable": op.NewTyped[batchDeleteTableReq, batchDeleteTableResp]("BatchDeleteTable", s.batchDeleteTableTyped),
		// Table versions
		"GetTableVersion":         op.NewTyped[getTableVersionReq, getTableVersionResp]("GetTableVersion", s.getTableVersionTyped),
		"GetTableVersions":        op.NewTyped[getTableVersionsReq, getTableVersionsResp]("GetTableVersions", s.getTableVersionsTyped),
		"DeleteTableVersion":      op.NewTyped[deleteTableVersionReq, struct{}]("DeleteTableVersion", s.deleteTableVersionTyped),
		"BatchDeleteTableVersion": op.NewTyped[batchDeleteTableVersionReq, batchDeleteTableVersionResp]("BatchDeleteTableVersion", s.batchDeleteTableVersionTyped),
		// Partitions
		"CreatePartition":      op.NewTyped[createPartitionReq, struct{}]("CreatePartition", s.createPartitionTyped),
		"BatchCreatePartition": op.NewTyped[batchCreatePartitionReq, batchPartitionErrorsResp]("BatchCreatePartition", s.batchCreatePartitionTyped),
		"GetPartition":         op.NewTyped[getPartitionReq, getPartitionResp]("GetPartition", s.getPartitionTyped),
		"GetPartitions":        op.NewTyped[getPartitionsReq, getPartitionsResp]("GetPartitions", s.getPartitionsTyped),
		"BatchGetPartition":    op.NewTyped[batchGetPartitionReq, batchGetPartitionResp]("BatchGetPartition", s.batchGetPartitionTyped),
		"UpdatePartition":      op.NewTyped[updatePartitionReq, struct{}]("UpdatePartition", s.updatePartitionTyped),
		"DeletePartition":      op.NewTyped[deletePartitionReq, struct{}]("DeletePartition", s.deletePartitionTyped),
		"BatchDeletePartition": op.NewTyped[batchDeletePartitionReq, batchPartitionErrorsResp]("BatchDeletePartition", s.batchDeletePartitionTyped),
		// Column statistics
		"UpdateColumnStatisticsForTable":     op.NewTyped[updateColumnStatisticsReq, updateColumnStatisticsResp]("UpdateColumnStatisticsForTable", s.updateColumnStatisticsForTableTyped),
		"UpdateColumnStatisticsForPartition": op.NewTyped[updateColumnStatisticsReq, updateColumnStatisticsResp]("UpdateColumnStatisticsForPartition", s.updateColumnStatisticsForPartitionTyped),
		"GetColumnStatisticsForTable":        op.NewTyped[getColumnStatisticsReq, getColumnStatisticsResp]("GetColumnStatisticsForTable", s.getColumnStatisticsForTableTyped),
		"GetColumnStatisticsForPartition":    op.NewTyped[getColumnStatisticsReq, getColumnStatisticsResp]("GetColumnStatisticsForPartition", s.getColumnStatisticsForPartitionTyped),
		"DeleteColumnStatisticsForTable":     op.NewTyped[deleteColumnStatisticsReq, struct{}]("DeleteColumnStatisticsForTable", s.deleteColumnStatisticsForTableTyped),
		"DeleteColumnStatisticsForPartition": op.NewTyped[deleteColumnStatisticsReq, struct{}]("DeleteColumnStatisticsForPartition", s.deleteColumnStatisticsForPartitionTyped),
		// Tags
		"TagResource":   op.NewTyped[glueTagResourceReq, struct{}]("TagResource", s.tagResourceTyped),
		"UntagResource": op.NewTyped[glueUntagResourceReq, struct{}]("UntagResource", s.untagResourceTyped),
		"GetTags":       op.NewTyped[glueListTagsForResourceReq, glueListTagsForResourceResp]("GetTags", s.listTagsForResourceTyped),
	}
}

func (s *Service) Operations() []op.Operation {
	ops := s.typedOp
	out := make([]op.Operation, 0, len(ops))
	for _, operation := range ops {
		out = append(out, operation)
	}
	return out
}

func (s *Service) SupportedProtocols() []codec.Codec {
	return []codec.Codec{codec.JSON10, codec.JSON11, codec.RPCv2CBOR}
}
