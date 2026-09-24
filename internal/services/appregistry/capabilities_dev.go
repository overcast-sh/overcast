//go:build dev

package appregistry

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		// Application lifecycle
		capabilities.Capability{Service: "appregistry", Operation: "CreateApplication", Category: "Application lifecycle",
			Status: capabilities.StatusSupported, Notes: "Auto-populates `applicationTag.awsApplication = <arn>` to match real AppRegistry. ID is a generated short identifier; ARN uses the standard `arn:aws:servicecatalog:...` format."},
		capabilities.Capability{Service: "appregistry", Operation: "GetApplication", Category: "Application lifecycle",
			Status: capabilities.StatusSupported, Notes: "Lookup accepts application name, ID, or ARN."},
		capabilities.Capability{Service: "appregistry", Operation: "ListApplications", Category: "Application lifecycle",
			Status: capabilities.StatusSupported, Notes: "No pagination (`nextToken` never returned)."},
		capabilities.Capability{Service: "appregistry", Operation: "DeleteApplication", Category: "Application lifecycle",
			Status: capabilities.StatusSupported, Notes: "Also removes all resource associations for the application."},
		capabilities.Capability{Service: "appregistry", Operation: "UpdateApplication", Category: "Application lifecycle",
			Status: capabilities.StatusSupported, Notes: "Updates `name` (with collision detection) and `description`; bumps `lastUpdateTime`."},

		// Resource associations
		capabilities.Capability{Service: "appregistry", Operation: "AssociateResource", Category: "Resource associations",
			Status: capabilities.StatusSupported, Notes: "Only `resourceType=CFN_STACK` is exercised today, but any resource type/ARN pair is accepted. URL-encoded resource ARN in the path."},
		capabilities.Capability{Service: "appregistry", Operation: "DisassociateResource", Category: "Resource associations",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appregistry", Operation: "ListAssociatedResources", Category: "Resource associations",
			Status: capabilities.StatusSupported, Notes: "No pagination."},
		capabilities.Capability{Service: "appregistry", Operation: "GetAssociatedResource", Category: "Resource associations",
			Status: capabilities.StatusSupported},

		// Attribute groups
		capabilities.Capability{Service: "appregistry", Operation: "CreateAttributeGroup", Category: "Attribute groups",
			Status: capabilities.StatusSupported, Notes: "Inert tier — attributes JSON is stored verbatim."},
		capabilities.Capability{Service: "appregistry", Operation: "GetAttributeGroup", Category: "Attribute groups",
			Status: capabilities.StatusSupported, Notes: "Lookup accepts name, ID, or ARN."},
		capabilities.Capability{Service: "appregistry", Operation: "ListAttributeGroups", Category: "Attribute groups",
			Status: capabilities.StatusSupported, Notes: "No pagination."},
		capabilities.Capability{Service: "appregistry", Operation: "UpdateAttributeGroup", Category: "Attribute groups",
			Status: capabilities.StatusSupported, Notes: "Name, description, and attributes can all be patched."},
		capabilities.Capability{Service: "appregistry", Operation: "DeleteAttributeGroup", Category: "Attribute groups",
			Status: capabilities.StatusSupported, Notes: "Does not cascade to associations."},
		capabilities.Capability{Service: "appregistry", Operation: "AssociateAttributeGroup", Category: "Attribute groups",
			Status: capabilities.StatusSupported, Notes: "Attribute group is accepted by name, ID, or ARN."},
		capabilities.Capability{Service: "appregistry", Operation: "DisassociateAttributeGroup", Category: "Attribute groups",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "appregistry", Operation: "ListAssociatedAttributeGroups", Category: "Attribute groups",
			Status: capabilities.StatusSupported, Notes: "Returns the associated attribute group IDs for an application. No pagination."},

		// Tagging
		capabilities.Capability{Service: "appregistry", Operation: "TagResource", Category: "Tagging",
			Status: capabilities.StatusSupported, Notes: "Inert tier — merges into the shared ARN-keyed tag store."},
		capabilities.Capability{Service: "appregistry", Operation: "UntagResource", Category: "Tagging",
			Status: capabilities.StatusSupported, Notes: "`tagKeys` removed from the stored map."},
		capabilities.Capability{Service: "appregistry", Operation: "ListTagsForResource", Category: "Tagging",
			Status: capabilities.StatusSupported, Notes: "Returns the stored map, or `{}` for an ARN with no recorded tags."},

		// CloudFormation resource types
		capabilities.Capability{Service: "appregistry", Operation: "AWS::ServiceCatalogAppRegistry::Application", Category: "CloudFormation resources",
			Status: capabilities.StatusSupported, Notes: "`GetAtt` attributes: `Id`, `Arn`, `Name`, `ApplicationName`, `ApplicationTagKey`, `ApplicationTagValue`. Tags merge with stack tags on create and reconcile via TagResource/UntagResource on update.", DocOnly: true},
		capabilities.Capability{Service: "appregistry", Operation: "AWS::ServiceCatalogAppRegistry::ResourceAssociation", Category: "CloudFormation resources",
			Status: capabilities.StatusSupported, Notes: "Physical ID is `<appId>/<resourceType>/<resource>`.", DocOnly: true},
		capabilities.Capability{Service: "appregistry", Operation: "AWS::ServiceCatalogAppRegistry::AttributeGroup", Category: "CloudFormation resources",
			Status: capabilities.StatusSupported, Notes: "`GetAtt` attributes: `Id`, `Arn`. Tags merge with stack tags on create and reconcile via TagResource/UntagResource on update.", DocOnly: true},
		capabilities.Capability{Service: "appregistry", Operation: "AWS::ServiceCatalogAppRegistry::AttributeGroupAssociation", Category: "CloudFormation resources",
			Status: capabilities.StatusSupported, Notes: "Physical ID is `<appId>/<attributeGroupId>`. Both properties are replacement-only, so Update always replaces.", DocOnly: true},
	)
}
