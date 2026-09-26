package glue

import (
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
)

func (s *Service) typedOps() map[string]op.Operation {
	// Every operation but the database and table reads and the tag
	// operations, which name no catalog, serves only the account's own
	// catalog: see ownCatalogOp.
	return map[string]op.Operation{
		// Catalogs
		"GetCatalog":  op.NewTyped[getCatalogReq, getCatalogResp]("GetCatalog", s.getCatalogTyped),
		"GetCatalogs": op.NewTyped[getCatalogsReq, getCatalogsResp]("GetCatalogs", s.getCatalogsTyped),
		// Databases
		"CreateDatabase": ownCatalogOp(s, "CreateDatabase", s.createDatabaseTyped),
		"GetDatabase":    op.NewTyped[getDatabaseReq, getDatabaseResp]("GetDatabase", s.getDatabaseTyped),
		"GetDatabases":   op.NewTyped[getDatabasesReq, getDatabasesResp]("GetDatabases", s.getDatabasesTyped),
		"UpdateDatabase": ownCatalogOp(s, "UpdateDatabase", s.updateDatabaseTyped),
		"DeleteDatabase": ownCatalogOp(s, "DeleteDatabase", s.deleteDatabaseTyped),
		// Tables
		"CreateTable":      ownCatalogOp(s, "CreateTable", s.createTableTyped),
		"GetTable":         op.NewTyped[getTableReq, getTableResp]("GetTable", s.getTableTyped),
		"GetTables":        op.NewTyped[getTablesReq, getTablesResp]("GetTables", s.getTablesTyped),
		"UpdateTable":      ownCatalogOp(s, "UpdateTable", s.updateTableTyped),
		"DeleteTable":      ownCatalogOp(s, "DeleteTable", s.deleteTableTyped),
		"BatchDeleteTable": ownCatalogOp(s, "BatchDeleteTable", s.batchDeleteTableTyped),
		// Table versions
		"GetTableVersion":         ownCatalogOp(s, "GetTableVersion", s.getTableVersionTyped),
		"GetTableVersions":        ownCatalogOp(s, "GetTableVersions", s.getTableVersionsTyped),
		"DeleteTableVersion":      ownCatalogOp(s, "DeleteTableVersion", s.deleteTableVersionTyped),
		"BatchDeleteTableVersion": ownCatalogOp(s, "BatchDeleteTableVersion", s.batchDeleteTableVersionTyped),
		// Partitions
		"CreatePartition":      ownCatalogOp(s, "CreatePartition", s.createPartitionTyped),
		"BatchCreatePartition": ownCatalogOp(s, "BatchCreatePartition", s.batchCreatePartitionTyped),
		"GetPartition":         ownCatalogOp(s, "GetPartition", s.getPartitionTyped),
		"GetPartitions":        ownCatalogOp(s, "GetPartitions", s.getPartitionsTyped),
		"BatchGetPartition":    ownCatalogOp(s, "BatchGetPartition", s.batchGetPartitionTyped),
		"UpdatePartition":      ownCatalogOp(s, "UpdatePartition", s.updatePartitionTyped),
		"DeletePartition":      ownCatalogOp(s, "DeletePartition", s.deletePartitionTyped),
		"BatchDeletePartition": ownCatalogOp(s, "BatchDeletePartition", s.batchDeletePartitionTyped),
		// Column statistics
		"UpdateColumnStatisticsForTable":     ownCatalogOp(s, "UpdateColumnStatisticsForTable", s.updateColumnStatisticsForTableTyped),
		"UpdateColumnStatisticsForPartition": ownCatalogOp(s, "UpdateColumnStatisticsForPartition", s.updateColumnStatisticsForPartitionTyped),
		"GetColumnStatisticsForTable":        ownCatalogOp(s, "GetColumnStatisticsForTable", s.getColumnStatisticsForTableTyped),
		"GetColumnStatisticsForPartition":    ownCatalogOp(s, "GetColumnStatisticsForPartition", s.getColumnStatisticsForPartitionTyped),
		"DeleteColumnStatisticsForTable":     ownCatalogOp(s, "DeleteColumnStatisticsForTable", s.deleteColumnStatisticsForTableTyped),
		"DeleteColumnStatisticsForPartition": ownCatalogOp(s, "DeleteColumnStatisticsForPartition", s.deleteColumnStatisticsForPartitionTyped),
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
