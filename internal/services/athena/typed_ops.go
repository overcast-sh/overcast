package athena

import (
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
)

func (s *Service) typedOps() map[string]op.Operation {
	return map[string]op.Operation{
		// Query executions
		"StartQueryExecution":       op.NewTyped[startQueryExecReq, startQueryExecResp]("StartQueryExecution", s.startQueryExecutionTyped),
		"GetQueryExecution":         op.NewTyped[queryIDReq, getQueryExecResp]("GetQueryExecution", s.getQueryExecutionTyped),
		"BatchGetQueryExecution":    op.NewTyped[batchGetQueryExecReq, batchGetQueryExecResp]("BatchGetQueryExecution", s.batchGetQueryExecutionTyped),
		"ListQueryExecutions":       op.NewTyped[listQueriesReq, listQueriesResp]("ListQueryExecutions", s.listQueryExecutionsTyped),
		"StopQueryExecution":        op.NewTyped[queryIDReq, struct{}]("StopQueryExecution", s.stopQueryExecutionTyped),
		"GetQueryResults":           op.NewTyped[getQueryResultsReq, getQueryResultsResp]("GetQueryResults", s.getQueryResultsTyped),
		"GetQueryRuntimeStatistics": op.NewTyped[queryIDReq, getQueryRuntimeStatisticsResp]("GetQueryRuntimeStatistics", s.getQueryRuntimeStatisticsTyped),
		// Workgroups and engine versions
		"CreateWorkGroup":    op.NewTyped[createWorkGroupReq, struct{}]("CreateWorkGroup", s.createWorkGroupTyped),
		"GetWorkGroup":       op.NewTyped[workGroupNameReq, getWorkGroupResp]("GetWorkGroup", s.getWorkGroupTyped),
		"ListWorkGroups":     op.NewTyped[listWorkGroupsReq, listWorkGroupsResp]("ListWorkGroups", s.listWorkGroupsTyped),
		"UpdateWorkGroup":    op.NewTyped[updateWorkGroupReq, struct{}]("UpdateWorkGroup", s.updateWorkGroupTyped),
		"DeleteWorkGroup":    op.NewTyped[deleteWorkGroupReq, struct{}]("DeleteWorkGroup", s.deleteWorkGroupTyped),
		"ListEngineVersions": op.NewTyped[listEngineVersionsReq, listEngineVersionsResp]("ListEngineVersions", s.listEngineVersionsTyped),
		// Named queries
		"CreateNamedQuery":   op.NewTyped[createNamedQueryReq, createNamedQueryResp]("CreateNamedQuery", s.createNamedQueryTyped),
		"GetNamedQuery":      op.NewTyped[namedQueryIDReq, getNamedQueryResp]("GetNamedQuery", s.getNamedQueryTyped),
		"BatchGetNamedQuery": op.NewTyped[batchGetNamedQueryReq, batchGetNamedQueryResp]("BatchGetNamedQuery", s.batchGetNamedQueryTyped),
		"ListNamedQueries":   op.NewTyped[listNamedQueriesReq, listNamedQueriesResp]("ListNamedQueries", s.listNamedQueriesTyped),
		"UpdateNamedQuery":   op.NewTyped[updateNamedQueryReq, struct{}]("UpdateNamedQuery", s.updateNamedQueryTyped),
		"DeleteNamedQuery":   op.NewTyped[namedQueryIDReq, struct{}]("DeleteNamedQuery", s.deleteNamedQueryTyped),
		// Prepared statements
		"CreatePreparedStatement":   op.NewTyped[preparedStatementReq, struct{}]("CreatePreparedStatement", s.createPreparedStatementTyped),
		"GetPreparedStatement":      op.NewTyped[preparedStatementNameReq, getPreparedStatementResp]("GetPreparedStatement", s.getPreparedStatementTyped),
		"BatchGetPreparedStatement": op.NewTyped[batchGetPreparedStatementReq, batchGetPreparedStatementResp]("BatchGetPreparedStatement", s.batchGetPreparedStatementTyped),
		"ListPreparedStatements":    op.NewTyped[listPreparedStatementsReq, listPreparedStatementsResp]("ListPreparedStatements", s.listPreparedStatementsTyped),
		"UpdatePreparedStatement":   op.NewTyped[preparedStatementReq, struct{}]("UpdatePreparedStatement", s.updatePreparedStatementTyped),
		"DeletePreparedStatement":   op.NewTyped[preparedStatementNameReq, struct{}]("DeletePreparedStatement", s.deletePreparedStatementTyped),
		// Data catalogs and the metadata they hold
		"CreateDataCatalog": op.NewTyped[createDataCatalogReq, dataCatalogResp]("CreateDataCatalog", s.createDataCatalogTyped),
		"GetDataCatalog":    op.NewTyped[getDataCatalogReq, dataCatalogResp]("GetDataCatalog", s.getDataCatalogTyped),
		"ListDataCatalogs":  op.NewTyped[listDataCatalogsReq, listDataCatalogsResp]("ListDataCatalogs", s.listDataCatalogsTyped),
		"UpdateDataCatalog": op.NewTyped[updateDataCatalogReq, struct{}]("UpdateDataCatalog", s.updateDataCatalogTyped),
		"DeleteDataCatalog": op.NewTyped[deleteDataCatalogReq, dataCatalogResp]("DeleteDataCatalog", s.deleteDataCatalogTyped),
		"GetDatabase":       op.NewTyped[catalogDatabaseReq, getDatabaseResp]("GetDatabase", s.getDatabaseTyped),
		"ListDatabases":     op.NewTyped[listDatabasesReq, listDatabasesResp]("ListDatabases", s.listDatabasesTyped),
		"GetTableMetadata":  op.NewTyped[getTableMetadataReq, getTableMetadataResp]("GetTableMetadata", s.getTableMetadataTyped),
		"ListTableMetadata": op.NewTyped[listTableMetadataReq, listTableMetadataResp]("ListTableMetadata", s.listTableMetadataTyped),
		// Tags
		"TagResource":         op.NewTyped[tagResourceReq, struct{}]("TagResource", s.tagResourceTyped),
		"UntagResource":       op.NewTyped[untagResourceReq, struct{}]("UntagResource", s.untagResourceTyped),
		"ListTagsForResource": op.NewTyped[listTagsForResourceReq, listTagsForResourceResp]("ListTagsForResource", s.listTagsForResourceTyped),
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
