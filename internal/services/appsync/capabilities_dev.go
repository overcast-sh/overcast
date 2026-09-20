//go:build dev

package appsync

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		// GraphQL APIs
		capabilities.Capability{Service: "appsync", Operation: "CreateGraphqlApi", Category: "GraphQL APIs",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "GetGraphqlApi", Category: "GraphQL APIs",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "ListGraphqlApis", Category: "GraphQL APIs",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "UpdateGraphqlApi", Category: "GraphQL APIs",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "DeleteGraphqlApi", Category: "GraphQL APIs",
			Status: capabilities.StatusSupported},

		// Schemas
		capabilities.Capability{Service: "appsync", Operation: "StartSchemaCreation", Category: "Schemas",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "GetSchemaCreationStatus", Category: "Schemas",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "GetIntrospectionSchema", Category: "Schemas",
			Status: capabilities.StatusSupported},

		// API Keys
		capabilities.Capability{Service: "appsync", Operation: "CreateApiKey", Category: "API Keys",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "ListApiKeys", Category: "API Keys",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "UpdateApiKey", Category: "API Keys",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "DeleteApiKey", Category: "API Keys",
			Status: capabilities.StatusSupported},

		// Data Sources
		capabilities.Capability{Service: "appsync", Operation: "CreateDataSource", Category: "Data Sources",
			Status: capabilities.StatusSupported, Notes: "AMAZON_DYNAMODB, AWS_LAMBDA, HTTP and NONE resolve at execution time; AMAZON_OPENSEARCH_SERVICE, AMAZON_ELASTICSEARCH, RELATIONAL_DATABASE, AMAZON_EVENTBRIDGE and AMAZON_BEDROCK_RUNTIME are accepted and stored only"},
		capabilities.Capability{Service: "appsync", Operation: "GetDataSource", Category: "Data Sources",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "ListDataSources", Category: "Data Sources",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "UpdateDataSource", Category: "Data Sources",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "DeleteDataSource", Category: "Data Sources",
			Status: capabilities.StatusSupported},

		// Functions
		capabilities.Capability{Service: "appsync", Operation: "CreateFunction", Category: "Functions",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "GetFunction", Category: "Functions",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "ListFunctions", Category: "Functions",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "UpdateFunction", Category: "Functions",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "DeleteFunction", Category: "Functions",
			Status: capabilities.StatusSupported},

		// Resolvers
		capabilities.Capability{Service: "appsync", Operation: "CreateResolver", Category: "Resolvers",
			Status: capabilities.StatusSupported, Notes: "UNIT and PIPELINE resolvers; requestMappingTemplate, responseMappingTemplate"},
		capabilities.Capability{Service: "appsync", Operation: "GetResolver", Category: "Resolvers",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "ListResolvers", Category: "Resolvers",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "UpdateResolver", Category: "Resolvers",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "DeleteResolver", Category: "Resolvers",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "ListResolversByFunction", Category: "Resolvers",
			Status: capabilities.StatusSupported},

		// Tags
		capabilities.Capability{Service: "appsync", Operation: "TagResource", Category: "Tags",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "UntagResource", Category: "Tags",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "ListTagsForResource", Category: "Tags",
			Status: capabilities.StatusSupported},

		// Environment Variables
		capabilities.Capability{Service: "appsync", Operation: "PutGraphqlApiEnvironmentVariables", Category: "Environment Variables",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "GetGraphqlApiEnvironmentVariables", Category: "Environment Variables",
			Status: capabilities.StatusSupported},

		// Domain Names
		capabilities.Capability{Service: "appsync", Operation: "CreateDomainName", Category: "Domain Names",
			Status: capabilities.StatusSupported, Notes: "Inert metadata; no routing effect"},
		capabilities.Capability{Service: "appsync", Operation: "GetDomainName", Category: "Domain Names",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "ListDomainNames", Category: "Domain Names",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "UpdateDomainName", Category: "Domain Names",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "DeleteDomainName", Category: "Domain Names",
			Status: capabilities.StatusSupported},

		// API Associations
		capabilities.Capability{Service: "appsync", Operation: "AssociateApi", Category: "API Associations",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "GetApiAssociation", Category: "API Associations",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "DisassociateApi", Category: "API Associations",
			Status: capabilities.StatusSupported},

		// API Cache
		capabilities.Capability{Service: "appsync", Operation: "CreateApiCache", Category: "API Cache",
			Status: capabilities.StatusSupported, Notes: "Config stored; no actual caching enforced"},
		capabilities.Capability{Service: "appsync", Operation: "GetApiCache", Category: "API Cache",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "UpdateApiCache", Category: "API Cache",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "DeleteApiCache", Category: "API Cache",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "FlushApiCache", Category: "API Cache",
			Status: capabilities.StatusSupported},

		// Types
		capabilities.Capability{Service: "appsync", Operation: "CreateType", Category: "Types",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "GetType", Category: "Types",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "ListTypes", Category: "Types",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "UpdateType", Category: "Types",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "DeleteType", Category: "Types",
			Status: capabilities.StatusSupported},

		// Merged APIs
		capabilities.Capability{Service: "appsync", Operation: "AssociateSourceGraphqlApi", Category: "Merged APIs",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "AssociateMergedGraphqlApi", Category: "Merged APIs",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "GetSourceApiAssociation", Category: "Merged APIs",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "ListSourceApiAssociations", Category: "Merged APIs",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "DisassociateSourceGraphqlApi", Category: "Merged APIs",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "DisassociateMergedGraphqlApi", Category: "Merged APIs",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "StartSchemaMerge", Category: "Merged APIs",
			Status: capabilities.StatusSupported},

		// Events API
		capabilities.Capability{Service: "appsync", Operation: "CreateApi", Category: "Events API",
			Status: capabilities.StatusSupported, Notes: "GRAPHQL and MERGED event API types"},
		capabilities.Capability{Service: "appsync", Operation: "GetApi", Category: "Events API",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "ListApis", Category: "Events API",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "UpdateApi", Category: "Events API",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "DeleteApi", Category: "Events API",
			Status: capabilities.StatusSupported},

		// Channel Namespaces
		capabilities.Capability{Service: "appsync", Operation: "CreateChannelNamespace", Category: "Channel Namespaces",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "GetChannelNamespace", Category: "Channel Namespaces",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "ListChannelNamespaces", Category: "Channel Namespaces",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "UpdateChannelNamespace", Category: "Channel Namespaces",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appsync", Operation: "DeleteChannelNamespace", Category: "Channel Namespaces",
			Status: capabilities.StatusSupported},

		// Execution & Evaluation
		capabilities.Capability{Service: "appsync", Operation: "ExecuteGraphQL", Category: "Execution & Evaluation",
			EmulatorOnly: true,
			Status:       capabilities.StatusSupported, Notes: "Executes a GraphQL operation against the API; Cognito bearer tokens are decoded only, or verified against the local user pool when OVERCAST_ENFORCE_APPSYNC_COGNITO_AUTH=true"},
		capabilities.Capability{Service: "appsync", Operation: "EvaluateMappingTemplate", Category: "Execution & Evaluation",
			Status: capabilities.StatusSupported, Notes: "Evaluates VTL mapping templates; logs and outErrors are not populated"},
		capabilities.Capability{Service: "appsync", Operation: "EvaluateCode", Category: "Execution & Evaluation",
			Status: capabilities.StatusSupported, Notes: "Evaluates APPSYNC_JS resolver code; outErrors is not populated"},

		// DynamoDB Resolver Operations
		capabilities.Capability{Service: "appsync", Operation: "GetItem", Category: "DynamoDB Resolver Operations",
			Status: capabilities.StatusSupported, Notes: "DynamoDB data source resolver operation", DocOnly: true},
		capabilities.Capability{Service: "appsync", Operation: "PutItem", Category: "DynamoDB Resolver Operations",
			Status: capabilities.StatusSupported, Notes: "DynamoDB data source resolver operation", DocOnly: true},
		capabilities.Capability{Service: "appsync", Operation: "DeleteItem", Category: "DynamoDB Resolver Operations",
			Status: capabilities.StatusSupported, Notes: "DynamoDB data source resolver operation", DocOnly: true},
		capabilities.Capability{Service: "appsync", Operation: "UpdateItem", Category: "DynamoDB Resolver Operations",
			Status: capabilities.StatusSupported, Notes: "DynamoDB data source resolver operation", DocOnly: true},
		capabilities.Capability{Service: "appsync", Operation: "Query", Category: "DynamoDB Resolver Operations",
			Status: capabilities.StatusSupported, Notes: "DynamoDB data source resolver operation", DocOnly: true},
		capabilities.Capability{Service: "appsync", Operation: "Scan", Category: "DynamoDB Resolver Operations",
			Status: capabilities.StatusSupported, Notes: "DynamoDB data source resolver operation", DocOnly: true},
		capabilities.Capability{Service: "appsync", Operation: "BatchGetItem", Category: "DynamoDB Resolver Operations",
			Status: capabilities.StatusSupported, Notes: "DynamoDB data source resolver operation", DocOnly: true},
		capabilities.Capability{Service: "appsync", Operation: "BatchWriteItem", Category: "DynamoDB Resolver Operations",
			Status: capabilities.StatusSupported, Notes: "DynamoDB data source resolver operation", DocOnly: true},
		capabilities.Capability{Service: "appsync", Operation: "TransactGetItems", Category: "DynamoDB Resolver Operations",
			Status: capabilities.StatusSupported, Notes: "DynamoDB data source resolver operation", DocOnly: true},
		capabilities.Capability{Service: "appsync", Operation: "TransactWriteItems", Category: "DynamoDB Resolver Operations",
			Status: capabilities.StatusSupported, Notes: "DynamoDB data source resolver operation", DocOnly: true},
		capabilities.Capability{Service: "appsync", Operation: "ConditionCheck", Category: "DynamoDB Resolver Operations",
			Status: capabilities.StatusSupported, Notes: "DynamoDB transact-write condition check", DocOnly: true},
	)
}
