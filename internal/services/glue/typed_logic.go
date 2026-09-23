package glue

import (
	"context"
	"errors"
	"maps"
	"regexp"
	"strconv"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Page-size limits. The catalog getters (GetDatabases, GetTables,
// GetTableVersions) are CatalogGetterPageSize, 1–100; GetPartitions is
// PageSize, 1–1000.
const (
	catalogPageSize   = 100
	partitionPageSize = 1000
)

// defaultTableType is what AWS gives a table created without a TableType
// through OpenTableFormatInput.
const defaultTableType = "EXTERNAL_TABLE"

// iamAllowedPrincipals is the CreateTableDefaultPermissions AWS returns for a
// database created without any, under the account's default Lake Formation
// settings.
func iamAllowedPrincipals() []any {
	return []any{map[string]any{
		"Principal":   map[string]any{"DataLakePrincipalIdentifier": "IAM_ALLOWED_PRINCIPALS"},
		"Permissions": []any{"ALL"},
	}}
}

func (s *Service) now() float64 { return float64(s.clk.Now().UnixMilli()) / 1000.0 }

func (s *Service) catalogID(requested string) string {
	if requested != "" {
		return requested
	}
	return s.cfg.AccountID
}

func paginate[T any](items []T, maxResults int32, token string, limit int) (serviceutil.Page[T], *protocol.AWSError) {
	page, err := serviceutil.Paginate(items, int(maxResults), token,
		serviceutil.PaginateOptions{DefaultLimit: limit, MaxLimit: limit})
	if errors.Is(err, serviceutil.ErrInvalidPageToken) {
		return page, errInvalidInput("Invalid NextToken.")
	}
	return page, nil
}

// requireDatabase loads a database or answers EntityNotFoundException, which
// is what every table and partition operation reports for a missing parent.
func (s *Service) requireDatabase(ctx context.Context, name string) (*databaseRecord, *protocol.AWSError) {
	if name == "" {
		return nil, errInvalidInput("DatabaseName is required.")
	}
	db, found, err := s.store.getDatabase(ctx, name)
	if err != nil {
		return nil, errInternal(err)
	}
	if !found {
		return nil, errDatabaseNotFound(name)
	}
	return db, nil
}

// requireTable loads a table, checking its database first as AWS does.
func (s *Service) requireTable(ctx context.Context, dbName, tableName string) (*tableRecord, *protocol.AWSError) {
	if _, aerr := s.requireDatabase(ctx, dbName); aerr != nil {
		return nil, aerr
	}
	if tableName == "" {
		return nil, errInvalidInput("Table name is required.")
	}
	t, found, err := s.store.getTable(ctx, dbName, tableName)
	if err != nil {
		return nil, errInternal(err)
	}
	if !found {
		return nil, errTableNotFound(tableName)
	}
	return t, nil
}

// lockTable takes the table's write lock and loads the table inside it, so
// the caller's write — UpdateTable, a version delete, any partition write —
// is serialised with every other write to the table and cannot land under a
// table that is no longer there. The caller runs the returned unlock.
// Partitions share their table's lock rather than taking one each, because
// UpdatePartition can move a partition to new values and so writes two keys
// at once.
func (s *Service) lockTable(ctx context.Context, dbName, tableName string) (*tableRecord, func(), *protocol.AWSError) {
	dbName, tableName = normName(dbName), normName(tableName)
	unlock := s.writeLock(tableLockKey(dbName, tableName))
	t, aerr := s.requireTable(ctx, dbName, tableName)
	if aerr != nil {
		unlock()
		return nil, nil, aerr
	}
	return t, unlock, nil
}

// ─── Databases ─────────────────────────────────────────────────

type createDatabaseReq struct {
	CatalogId     string            `json:"CatalogId" cbor:"CatalogId"`
	DatabaseInput *DatabaseInput    `json:"DatabaseInput" cbor:"DatabaseInput"`
	Tags          map[string]string `json:"Tags" cbor:"Tags"`
}

type getDatabaseReq struct {
	CatalogId string `json:"CatalogId" cbor:"CatalogId"`
	Name      string `json:"Name" cbor:"Name"`
}

type getDatabaseResp struct {
	Database *Database `json:"Database" cbor:"Database"`
}

type getDatabasesReq struct {
	CatalogId  string `json:"CatalogId" cbor:"CatalogId"`
	MaxResults int32  `json:"MaxResults" cbor:"MaxResults"`
	NextToken  string `json:"NextToken" cbor:"NextToken"`
}

type getDatabasesResp struct {
	DatabaseList []*Database `json:"DatabaseList" cbor:"DatabaseList"`
	NextToken    string      `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
}

type updateDatabaseReq struct {
	CatalogId     string         `json:"CatalogId" cbor:"CatalogId"`
	Name          string         `json:"Name" cbor:"Name"`
	DatabaseInput *DatabaseInput `json:"DatabaseInput" cbor:"DatabaseInput"`
}

type deleteDatabaseReq struct {
	CatalogId string `json:"CatalogId" cbor:"CatalogId"`
	Name      string `json:"Name" cbor:"Name"`
}

// databaseFromInput builds the stored definition. UpdateDatabase replaces a
// database's definition wholesale, as CreateDatabase sets it.
func databaseFromInput(in *DatabaseInput, name, catalogID string) Database {
	perms := in.CreateTableDefaultPermissions
	if perms == nil {
		perms = iamAllowedPrincipals()
	}
	return Database{
		Name:                          name,
		Description:                   in.Description,
		LocationUri:                   in.LocationUri,
		Parameters:                    in.Parameters,
		CreateTableDefaultPermissions: perms,
		TargetDatabase:                in.TargetDatabase,
		FederatedDatabase:             in.FederatedDatabase,
		CatalogId:                     catalogID,
	}
}

func (s *Service) createDatabaseTyped(ctx context.Context, req *createDatabaseReq) (*struct{}, *protocol.AWSError) {
	if req.DatabaseInput == nil || req.DatabaseInput.Name == "" {
		return nil, errInvalidInput("DatabaseInput.Name is required.")
	}
	name := normName(req.DatabaseInput.Name)
	if len(req.Tags) > 0 {
		if aerr := serviceutil.ValidateTags(glueTagCfg, req.Tags); aerr != nil {
			return nil, aerr
		}
	}
	defer s.writeLock(databaseLockKey(name))()
	_, found, err := s.store.getDatabase(ctx, name)
	if err != nil {
		return nil, errInternal(err)
	}
	if found {
		return nil, glueError(codeAlreadyExists, "Database already exists.")
	}
	db := databaseFromInput(req.DatabaseInput, name, s.catalogID(req.CatalogId))
	db.CreateTime = s.now()
	rec := &databaseRecord{Database: db}
	if len(req.Tags) > 0 {
		rec.Tags = maps.Clone(req.Tags)
	}
	if err := s.store.putDatabase(ctx, rec); err != nil {
		return nil, errInternal(err)
	}
	return &struct{}{}, nil
}

func (s *Service) getDatabaseTyped(ctx context.Context, req *getDatabaseReq) (*getDatabaseResp, *protocol.AWSError) {
	db, aerr := s.requireDatabase(ctx, normName(req.Name))
	if aerr != nil {
		return nil, aerr
	}
	return &getDatabaseResp{Database: &db.Database}, nil
}

func (s *Service) getDatabasesTyped(ctx context.Context, req *getDatabasesReq) (*getDatabasesResp, *protocol.AWSError) {
	records, err := s.store.listDatabases(ctx)
	if err != nil {
		return nil, errInternal(err)
	}
	page, aerr := paginate(records, req.MaxResults, req.NextToken, catalogPageSize)
	if aerr != nil {
		return nil, aerr
	}
	dbs := make([]*Database, 0, len(page.Items))
	for _, rec := range page.Items {
		dbs = append(dbs, &rec.Database)
	}
	return &getDatabasesResp{DatabaseList: dbs, NextToken: page.NextToken}, nil
}

func (s *Service) updateDatabaseTyped(ctx context.Context, req *updateDatabaseReq) (*struct{}, *protocol.AWSError) {
	if req.DatabaseInput == nil {
		return nil, errInvalidInput("DatabaseInput is required.")
	}
	name := normName(req.Name)
	// Glue has no database rename: Name picks the database, and a
	// DatabaseInput naming another one is refused rather than ignored.
	if in := normName(req.DatabaseInput.Name); in != "" && in != name {
		return nil, errInvalidInput("Renaming a database is not supported: DatabaseInput.Name %s does not match Name %s.", in, name)
	}
	defer s.writeLock(databaseLockKey(name))()
	cur, aerr := s.requireDatabase(ctx, name)
	if aerr != nil {
		return nil, aerr
	}
	db := databaseFromInput(req.DatabaseInput, name, cur.CatalogId)
	db.CreateTime = cur.CreateTime
	if err := s.store.putDatabase(ctx, &databaseRecord{Database: db, Tags: cur.Tags}); err != nil {
		return nil, errInternal(err)
	}
	return &struct{}{}, nil
}

// deleteDatabaseTyped removes the database and everything in it. AWS: "After
// completing this operation, you no longer have access to the tables (and all
// table versions and partitions that might belong to the tables)"; it
// reclaims them asynchronously, Overcast at once.
func (s *Service) deleteDatabaseTyped(ctx context.Context, req *deleteDatabaseReq) (*struct{}, *protocol.AWSError) {
	name := normName(req.Name)
	defer s.cascadeLock()()
	if _, aerr := s.requireDatabase(ctx, name); aerr != nil {
		return nil, aerr
	}
	if err := s.store.deleteDatabase(ctx, name); err != nil {
		return nil, errInternal(err)
	}
	return &struct{}{}, nil
}

// ─── Tables ────────────────────────────────────────────────────

// openTableFormatInput is accepted so a create that carries it is not
// refused, but Overcast does not write Iceberg metadata; see createTableResp.
type openTableFormatInput struct {
	IcebergInput map[string]any `json:"IcebergInput,omitempty" cbor:"IcebergInput,omitempty"`
}

type createTableReq struct {
	CatalogId            string                `json:"CatalogId" cbor:"CatalogId"`
	DatabaseName         string                `json:"DatabaseName" cbor:"DatabaseName"`
	Name                 string                `json:"Name" cbor:"Name"`
	TableInput           *TableInput           `json:"TableInput" cbor:"TableInput"`
	OpenTableFormatInput *openTableFormatInput `json:"OpenTableFormatInput" cbor:"OpenTableFormatInput"`
}

// icebergMetadataLimitation is marked on a CreateTable that asked AWS to
// write the table's initial Iceberg metadata.
const icebergMetadataLimitation = "OpenTableFormatInput.IcebergInput is accepted, but no Iceberg metadata is written: the table has no metadata_location."

// createTableResp is CreateTable's empty output. It carries the limitation
// when the request asked for Iceberg metadata Overcast does not write.
type createTableResp struct {
	limitation string
}

func (r *createTableResp) EmulationLimitations() []string {
	if r.limitation == "" {
		return nil
	}
	return []string{r.limitation}
}

type getTableReq struct {
	CatalogId    string `json:"CatalogId" cbor:"CatalogId"`
	DatabaseName string `json:"DatabaseName" cbor:"DatabaseName"`
	Name         string `json:"Name" cbor:"Name"`
}

type getTableResp struct {
	Table *Table `json:"Table" cbor:"Table"`
}

type getTablesReq struct {
	CatalogId    string `json:"CatalogId" cbor:"CatalogId"`
	DatabaseName string `json:"DatabaseName" cbor:"DatabaseName"`
	Expression   string `json:"Expression" cbor:"Expression"`
	MaxResults   int32  `json:"MaxResults" cbor:"MaxResults"`
	NextToken    string `json:"NextToken" cbor:"NextToken"`
}

type getTablesResp struct {
	TableList []*Table `json:"TableList" cbor:"TableList"`
	NextToken string   `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
}

type updateTableReq struct {
	CatalogId                  string         `json:"CatalogId" cbor:"CatalogId"`
	DatabaseName               string         `json:"DatabaseName" cbor:"DatabaseName"`
	Name                       string         `json:"Name" cbor:"Name"`
	TableInput                 *TableInput    `json:"TableInput" cbor:"TableInput"`
	SkipArchive                *bool          `json:"SkipArchive" cbor:"SkipArchive"`
	VersionId                  string         `json:"VersionId" cbor:"VersionId"`
	UpdateOpenTableFormatInput map[string]any `json:"UpdateOpenTableFormatInput" cbor:"UpdateOpenTableFormatInput"`
}

type deleteTableReq struct {
	CatalogId    string `json:"CatalogId" cbor:"CatalogId"`
	DatabaseName string `json:"DatabaseName" cbor:"DatabaseName"`
	Name         string `json:"Name" cbor:"Name"`
}

type batchDeleteTableReq struct {
	CatalogId      string   `json:"CatalogId" cbor:"CatalogId"`
	DatabaseName   string   `json:"DatabaseName" cbor:"DatabaseName"`
	TablesToDelete []string `json:"TablesToDelete" cbor:"TablesToDelete"`
}

type batchDeleteTableResp struct {
	Errors []TableError `json:"Errors" cbor:"Errors"`
}

// maxBatchDeleteTables is BatchDeleteTableNameList's length limit.
const maxBatchDeleteTables = 100

// tableFromInput builds a table's stored definition. UpdateTable replaces a
// table's definition wholesale, as CreateTable sets it.
func tableFromInput(in *TableInput, dbName, name, catalogID string) Table {
	return Table{
		Name:              name,
		DatabaseName:      dbName,
		Description:       in.Description,
		Owner:             in.Owner,
		LastAccessTime:    in.LastAccessTime,
		LastAnalyzedTime:  in.LastAnalyzedTime,
		Retention:         in.Retention,
		StorageDescriptor: in.StorageDescriptor,
		PartitionKeys:     in.PartitionKeys,
		ViewOriginalText:  in.ViewOriginalText,
		ViewExpandedText:  in.ViewExpandedText,
		TableType:         in.TableType,
		Parameters:        in.Parameters,
		TargetTable:       in.TargetTable,
		ViewDefinition:    in.ViewDefinition,
		CatalogId:         catalogID,
	}
}

func (s *Service) createTableTyped(ctx context.Context, req *createTableReq) (*createTableResp, *protocol.AWSError) {
	iceberg := req.OpenTableFormatInput != nil && req.OpenTableFormatInput.IcebergInput != nil
	in := req.TableInput
	if in == nil {
		if !iceberg {
			return nil, errInvalidInput("TableInput is required.")
		}
		in = &TableInput{}
	}
	name := in.Name
	if name == "" {
		name = req.Name
	}
	if name == "" {
		return nil, errInvalidInput("TableInput.Name is required.")
	}
	name = normName(name)
	dbName := normName(req.DatabaseName)

	defer s.writeLock(tableLockKey(dbName, name))()
	if _, aerr := s.requireDatabase(ctx, dbName); aerr != nil {
		return nil, aerr
	}
	_, found, err := s.store.getTable(ctx, dbName, name)
	if err != nil {
		return nil, errInternal(err)
	}
	if found {
		return nil, glueError(codeAlreadyExists, "Table already exists.")
	}
	// A table that is not found may still have left partitions or versions
	// behind — its record was corrupt, or a crash interrupted its delete. A
	// new table must not inherit them.
	if err := s.store.deleteTableChildren(ctx, dbName, name); err != nil {
		return nil, errInternal(err)
	}

	t := tableFromInput(in, dbName, name, s.catalogID(req.CatalogId))
	resp := &createTableResp{}
	if iceberg {
		// What AWS derives from IcebergInput and Overcast can: the table is
		// an external Iceberg table. The metadata file and its
		// metadata_location are the part Overcast does not produce.
		t.Parameters = maps.Clone(t.Parameters)
		if t.Parameters == nil {
			t.Parameters = map[string]string{}
		}
		if _, ok := t.Parameters["table_type"]; !ok {
			t.Parameters["table_type"] = "ICEBERG"
		}
		if t.TableType == "" {
			t.TableType = defaultTableType
		}
		resp.limitation = icebergMetadataLimitation
	}
	now := s.now()
	t.CreateTime, t.UpdateTime = now, now
	t.VersionId = initialVersionID
	t.normalize()
	if err := s.store.putTable(ctx, &tableRecord{Table: t}); err != nil {
		return nil, errInternal(err)
	}
	return resp, nil
}

func (s *Service) getTableTyped(ctx context.Context, req *getTableReq) (*getTableResp, *protocol.AWSError) {
	t, aerr := s.requireTable(ctx, normName(req.DatabaseName), normName(req.Name))
	if aerr != nil {
		return nil, aerr
	}
	return &getTableResp{Table: &t.Table}, nil
}

func (s *Service) getTablesTyped(ctx context.Context, req *getTablesReq) (*getTablesResp, *protocol.AWSError) {
	dbName := normName(req.DatabaseName)
	if _, aerr := s.requireDatabase(ctx, dbName); aerr != nil {
		return nil, aerr
	}
	// Expression is "a regular expression pattern. If present, only those
	// tables whose names match the pattern are returned." It must match the
	// whole name.
	var re *regexp.Regexp
	if req.Expression != "" {
		var err error
		if re, err = regexp.Compile("^(?:" + req.Expression + ")$"); err != nil {
			return nil, errInvalidInput("Invalid Expression %q: %v", req.Expression, err)
		}
	}
	records, err := s.store.listTables(ctx, dbName)
	if err != nil {
		return nil, errInternal(err)
	}
	tables := make([]*Table, 0, len(records))
	for _, rec := range records {
		if re == nil || re.MatchString(rec.Name) {
			tables = append(tables, &rec.Table)
		}
	}
	page, aerr := paginate(tables, req.MaxResults, req.NextToken, catalogPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &getTablesResp{TableList: page.Items, NextToken: page.NextToken}, nil
}

// nextVersionID is the version UpdateTable gives a table whose current
// version is cur.
func nextVersionID(cur string) string {
	n, err := strconv.ParseInt(cur, 10, 64)
	if err != nil {
		return initialVersionID
	}
	return strconv.FormatInt(n+1, 10)
}

// updateTableTyped replaces a table's definition. VersionId, when given, is
// the version the caller read: a table that has moved on since is a
// ConcurrentModificationException, which is how Iceberg's Glue catalog
// detects a lost commit race. Unless SkipArchive is set, the replaced
// definition is kept as a table version.
func (s *Service) updateTableTyped(ctx context.Context, req *updateTableReq) (*struct{}, *protocol.AWSError) {
	if req.UpdateOpenTableFormatInput != nil {
		return nil, &protocol.AWSError{
			Code:       protocol.ErrNotImplemented.Code,
			Message:    "UpdateOpenTableFormatInput is not implemented: Overcast does not write Iceberg metadata.",
			HTTPStatus: protocol.ErrNotImplemented.HTTPStatus,
		}
	}
	if req.TableInput == nil {
		return nil, errInvalidInput("TableInput is required.")
	}
	name := normName(req.Name)
	inName := normName(req.TableInput.Name)
	switch {
	case name == "":
		name = inName
	case inName != "" && inName != name:
		return nil, errInvalidInput("Renaming a table is not supported: TableInput.Name %s does not match Name %s.", inName, name)
	}
	if name == "" {
		return nil, errInvalidInput("TableInput.Name is required.")
	}
	dbName := normName(req.DatabaseName)

	cur, unlock, aerr := s.lockTable(ctx, dbName, name)
	if aerr != nil {
		return nil, aerr
	}
	defer unlock()
	if req.VersionId != "" && req.VersionId != cur.VersionId {
		return nil, glueError(codeConcurrentModification,
			"Update table failed due to concurrent modifications: version %s is not the current version %s.", req.VersionId, cur.VersionId)
	}
	if req.SkipArchive == nil || !*req.SkipArchive {
		archived := cur.Table
		if err := s.store.putTableVersion(ctx, &archived); err != nil {
			return nil, errInternal(err)
		}
	}
	t := tableFromInput(req.TableInput, dbName, name, cur.CatalogId)
	t.CreateTime = cur.CreateTime
	t.UpdateTime = s.now()
	t.VersionId = nextVersionID(cur.VersionId)
	t.normalize()
	if err := s.store.putTable(ctx, &tableRecord{Table: t, Tags: cur.Tags}); err != nil {
		return nil, errInternal(err)
	}
	return &struct{}{}, nil
}

// deleteTableTyped removes the table with its partitions and versions, which
// AWS also removes (asynchronously).
func (s *Service) deleteTableTyped(ctx context.Context, req *deleteTableReq) (*struct{}, *protocol.AWSError) {
	dbName, name := normName(req.DatabaseName), normName(req.Name)
	defer s.cascadeLock()()
	if _, aerr := s.requireDatabase(ctx, dbName); aerr != nil {
		return nil, aerr
	}
	if name == "" {
		return nil, errInvalidInput("Table name is required.")
	}
	if aerr := s.deleteOneTable(ctx, dbName, name); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (s *Service) batchDeleteTableTyped(ctx context.Context, req *batchDeleteTableReq) (*batchDeleteTableResp, *protocol.AWSError) {
	if len(req.TablesToDelete) > maxBatchDeleteTables {
		return nil, errInvalidInput("TablesToDelete must hold at most %d names.", maxBatchDeleteTables)
	}
	dbName := normName(req.DatabaseName)
	defer s.cascadeLock()()
	if _, aerr := s.requireDatabase(ctx, dbName); aerr != nil {
		return nil, aerr
	}
	resp := &batchDeleteTableResp{Errors: []TableError{}}
	for _, raw := range req.TablesToDelete {
		if aerr := s.deleteOneTable(ctx, dbName, normName(raw)); aerr != nil {
			resp.Errors = append(resp.Errors, TableError{TableName: raw, ErrorDetail: errorDetail(aerr)})
		}
	}
	return resp, nil
}

// deleteOneTable deletes one table of an existing database, cascading to its
// partitions and archived versions. The caller holds cascadeLock.
//
// Existence is the raw record's, not a decodable one's: a corrupt table must
// still be deletable, or its partitions and versions would outlive it.
func (s *Service) deleteOneTable(ctx context.Context, dbName, name string) *protocol.AWSError {
	found, err := s.store.tableExists(ctx, dbName, name)
	if err != nil {
		return errInternal(err)
	}
	if !found {
		return errTableNotFound(name)
	}
	if err := s.store.deleteTable(ctx, dbName, name); err != nil {
		return errInternal(err)
	}
	return nil
}
