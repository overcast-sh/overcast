package athena

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// awsDataCatalog is the account's built-in Glue Data Catalog: "of which you
// can have only one and cannot modify."
const awsDataCatalog = "AwsDataCatalog"

// Data catalog types (DataCatalogType).
const (
	catalogTypeGlue      = "GLUE"
	catalogTypeLambda    = "LAMBDA"
	catalogTypeHive      = "HIVE"
	catalogTypeFederated = "FEDERATED"
)

// catalogStatusComplete is the status of a catalog created synchronously —
// every type but FEDERATED. catalogStatusDeleted is what DeleteDataCatalog
// reports for one it removed.
const (
	catalogStatusComplete = "CREATE_COMPLETE"
	catalogStatusDeleted  = "DELETE_COMPLETE"
)

// catalogNamePattern: "can use a maximum of 127 alphanumeric, underscore, at
// sign, or hyphen characters."
var catalogNamePattern = regexp.MustCompile(`^[A-Za-z0-9_@-]{1,127}$`)

func isBuiltinCatalog(name string) bool { return strings.EqualFold(name, awsDataCatalog) }

func dataCatalogLockKey(name string) string { return "data-catalog:" + name }

// builtinCatalog is AwsDataCatalog as GetDataCatalog reports it.
func (s *Service) builtinCatalog() *DataCatalog {
	return &DataCatalog{
		Name: awsDataCatalog, Type: catalogTypeGlue, Status: catalogStatusComplete,
		Parameters: map[string]string{"catalog-id": s.cfg.AccountID},
	}
}

func (c *DataCatalog) summary() DataCatalogSummary {
	return DataCatalogSummary{CatalogName: c.Name, Type: c.Type, Status: c.Status}
}

// validateCatalogDefinition checks Type and the Parameters that type
// requires, per CreateDataCatalog's documentation of each.
func validateCatalogDefinition(catalogType string, params map[string]string) *protocol.AWSError {
	has := func(k string) bool { return params[k] != "" }
	switch catalogType {
	case "":
		return errRequired("Type")
	case catalogTypeGlue:
		if !has("catalog-id") {
			return errInvalidRequest("A GLUE data catalog requires the catalog-id parameter.")
		}
	case catalogTypeHive:
		if !has("metadata-function") {
			return errInvalidRequest("A HIVE data catalog requires the metadata-function parameter.")
		}
	case catalogTypeLambda:
		if has("function") == (has("metadata-function") && has("record-function")) {
			return errInvalidRequest("A LAMBDA data catalog requires either function, or both metadata-function and record-function.")
		}
	case catalogTypeFederated:
		return errNotEmulated("FEDERATED data catalogs create a Glue connection and a Lambda connector, which Overcast does not emulate.")
	default:
		return errInvalidRequest("Type must be one of %s, %s, %s or %s.", catalogTypeLambda, catalogTypeGlue, catalogTypeHive, catalogTypeFederated)
	}
	return nil
}

type createDataCatalogReq struct {
	Name        string                `json:"Name"`
	Type        string                `json:"Type"`
	Description string                `json:"Description"`
	Parameters  map[string]string     `json:"Parameters"`
	Tags        []serviceutil.TagPair `json:"Tags"`
}

type dataCatalogResp struct {
	DataCatalog DataCatalog `json:"DataCatalog"`
}

type getDataCatalogReq struct {
	Name      string `json:"Name"`
	WorkGroup string `json:"WorkGroup"`
}

type listDataCatalogsReq struct {
	MaxResults int32  `json:"MaxResults"`
	NextToken  string `json:"NextToken"`
	WorkGroup  string `json:"WorkGroup"`
}

type listDataCatalogsResp struct {
	DataCatalogsSummary []DataCatalogSummary `json:"DataCatalogsSummary"`
	NextToken           string               `json:"NextToken,omitempty"`
}

type updateDataCatalogReq struct {
	Name        string            `json:"Name"`
	Type        string            `json:"Type"`
	Description *string           `json:"Description"`
	Parameters  map[string]string `json:"Parameters"`
}

type deleteDataCatalogReq struct {
	Name              string `json:"Name"`
	DeleteCatalogOnly bool   `json:"DeleteCatalogOnly"`
}

func (s *Service) createDataCatalogTyped(ctx context.Context, req *createDataCatalogReq) (*dataCatalogResp, *protocol.AWSError) {
	if !catalogNamePattern.MatchString(req.Name) {
		return nil, errInvalidRequest("DataCatalog name %q is not valid: use at most 127 alphanumeric, underscore, at sign or hyphen characters.", req.Name)
	}
	if aerr := validateCatalogDefinition(req.Type, req.Parameters); aerr != nil {
		return nil, aerr
	}
	tags := serviceutil.TagsFromList(req.Tags)
	if aerr := serviceutil.ValidateTags(athenaTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	defer s.lock(dataCatalogLockKey(req.Name))()
	existing, err := s.store.getDataCatalog(ctx, req.Name)
	if err != nil {
		return nil, errInternal(err)
	}
	if existing != nil || isBuiltinCatalog(req.Name) {
		return nil, errInvalidRequest("DataCatalog %s already exists.", req.Name)
	}
	rec := &dataCatalogRecord{
		DataCatalog: DataCatalog{
			Name: req.Name, Type: req.Type, Description: req.Description,
			Parameters: req.Parameters, Status: catalogStatusComplete,
		},
		Tags: tags,
	}
	if err := s.store.putDataCatalog(ctx, rec); err != nil {
		return nil, errInternal(err)
	}
	return &dataCatalogResp{DataCatalog: rec.DataCatalog}, nil
}

// requireDataCatalog resolves a catalog name, AwsDataCatalog included.
func (s *Service) requireDataCatalog(ctx context.Context, name string) (*DataCatalog, *protocol.AWSError) {
	if name == "" {
		return nil, errRequired("Name")
	}
	if isBuiltinCatalog(name) {
		return s.builtinCatalog(), nil
	}
	rec, err := s.store.getDataCatalog(ctx, name)
	if err != nil {
		return nil, errInternal(err)
	}
	if rec == nil {
		return nil, errDataCatalogNotFound(name)
	}
	return &rec.DataCatalog, nil
}

// requireCustomCatalog loads a catalog a caller may change: any but
// AwsDataCatalog.
func (s *Service) requireCustomCatalog(ctx context.Context, name string) (*dataCatalogRecord, *protocol.AWSError) {
	if isBuiltinCatalog(name) {
		return nil, errInvalidRequest("%s cannot be modified or deleted.", awsDataCatalog)
	}
	if name == "" {
		return nil, errRequired("Name")
	}
	rec, err := s.store.getDataCatalog(ctx, name)
	if err != nil {
		return nil, errInternal(err)
	}
	if rec == nil {
		return nil, errDataCatalogNotFound(name)
	}
	return rec, nil
}

func (s *Service) getDataCatalogTyped(ctx context.Context, req *getDataCatalogReq) (*dataCatalogResp, *protocol.AWSError) {
	c, aerr := s.requireDataCatalog(ctx, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	return &dataCatalogResp{DataCatalog: *c}, nil
}

// listDataCatalogsTyped lists AwsDataCatalog first, then the registered
// catalogs by name.
func (s *Service) listDataCatalogsTyped(ctx context.Context, req *listDataCatalogsReq) (*listDataCatalogsResp, *protocol.AWSError) {
	records, err := scan[dataCatalogRecord](ctx, s.store, nsDataCatalogs, "")
	if err != nil {
		return nil, errInternal(err)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	summaries := make([]DataCatalogSummary, 0, len(records)+1)
	summaries = append(summaries, s.builtinCatalog().summary())
	for _, rec := range records {
		summaries = append(summaries, rec.summary())
	}
	page, aerr := paginate(summaries, req.MaxResults, req.NextToken, dataCatalogsPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &listDataCatalogsResp{DataCatalogsSummary: page.Items, NextToken: page.NextToken}, nil
}

func (s *Service) updateDataCatalogTyped(ctx context.Context, req *updateDataCatalogReq) (*struct{}, *protocol.AWSError) {
	defer s.lock(dataCatalogLockKey(req.Name))()
	rec, aerr := s.requireCustomCatalog(ctx, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	params := rec.Parameters
	if req.Parameters != nil {
		params = req.Parameters
	}
	if aerr := validateCatalogDefinition(req.Type, params); aerr != nil {
		return nil, aerr
	}
	rec.Type, rec.Parameters = req.Type, params
	if req.Description != nil {
		rec.Description = *req.Description
	}
	if err := s.store.putDataCatalog(ctx, rec); err != nil {
		return nil, errInternal(err)
	}
	return &struct{}{}, nil
}

func (s *Service) deleteDataCatalogTyped(ctx context.Context, req *deleteDataCatalogReq) (*dataCatalogResp, *protocol.AWSError) {
	defer s.lock(dataCatalogLockKey(req.Name))()
	rec, aerr := s.requireCustomCatalog(ctx, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	if err := s.store.delete(ctx, nsDataCatalogs, req.Name); err != nil {
		return nil, errInternal(err)
	}
	deleted := rec.DataCatalog
	deleted.Status = catalogStatusDeleted
	return &dataCatalogResp{DataCatalog: deleted}, nil
}
