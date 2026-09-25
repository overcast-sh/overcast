package athena

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/services/glue"
)

// ddl_partitions.go — ALTER TABLE ADD/DROP PARTITION, MSCK REPAIR TABLE and
// SHOW PARTITIONS: a Hive table's partitions as Glue partitions.
//
// A partition's data lives, by Hive's convention, under the table's location
// in one directory per key: s3://bucket/table/year=2026/month=09/. That is
// where ADD PARTITION puts one it is given no LOCATION for, and what MSCK
// REPAIR TABLE looks for.

// orderedValues returns spec's values in the table's key order, or a failure
// when spec does not name each key once.
func orderedValues(t glue.Table, spec partitionSpec) ([]string, *queryFailure) {
	if len(spec.Keys) != len(t.PartitionKeys) {
		return nil, failure(errorCategoryUser, errorTypeDDLFailed,
			"FAILED: SemanticException Partition spec does not match the partition keys of table "+t.Name)
	}
	values := make([]string, len(t.PartitionKeys))
	for i, key := range t.PartitionKeys {
		j := slices.IndexFunc(spec.Keys, func(k string) bool { return strings.EqualFold(k, key.Name) })
		if j < 0 {
			return nil, failure(errorCategoryUser, errorTypeDDLFailed,
				"FAILED: SemanticException Partition spec is missing key "+key.Name)
		}
		values[i] = spec.Values[j]
	}
	return values, nil
}

// partitionPath is a partition's directory under its table: key=value/…/,
// each value escaped as Hive escapes it.
func partitionPath(t glue.Table, values []string) string {
	var b strings.Builder
	for i, key := range t.PartitionKeys {
		b.WriteString(strings.ToLower(key.Name) + "=" + url.PathEscape(values[i]) + "/")
	}
	return b.String()
}

// partitionInput is a partition of t at location, stored as t is.
func partitionInput(t glue.Table, values []string, location string) glue.PartitionInput {
	sd := glue.StorageDescriptor{}
	if t.StorageDescriptor != nil {
		sd = *t.StorageDescriptor
	}
	sd.Location = location
	return glue.PartitionInput{Values: values, StorageDescriptor: &sd}
}

func tableLocation(t glue.Table) string {
	if t.StorageDescriptor == nil || t.StorageDescriptor.Location == "" {
		return ""
	}
	return strings.TrimSuffix(t.StorageDescriptor.Location, "/") + "/"
}

func (s *alterPartitionsStmt) run(ctx context.Context, env ddlEnv) (*queryResult, *queryFailure) {
	ref := env.resolve(s.Table)
	t, fail := env.requireTable(ctx, ref)
	if fail != nil {
		return nil, fail
	}
	values := make([][]string, len(s.Partitions))
	inputs := make([]glue.PartitionInput, len(s.Partitions))
	for i, spec := range s.Partitions {
		if values[i], fail = orderedValues(t, spec); fail != nil {
			return nil, fail
		}
		location := spec.Location
		if location == "" {
			location = tableLocation(t) + partitionPath(t, values[i])
		}
		inputs[i] = partitionInput(t, values[i], location)
	}
	var errs []glue.PartitionError
	var aerr *protocol.AWSError
	if s.Drop {
		errs, aerr = env.writer.DeletePartitions(ctx, ref.Database, ref.Table, values)
	} else {
		errs, aerr = env.writer.CreatePartitions(ctx, ref.Database, ref.Table, inputs)
	}
	if aerr != nil {
		return nil, catalogFailure(aerr)
	}
	for _, e := range errs {
		if !s.IfGuard && e.ErrorDetail != nil {
			return nil, catalogFailure(&protocol.AWSError{Code: e.ErrorDetail.ErrorCode, Message: e.ErrorDetail.ErrorMessage})
		}
	}
	return emptyResult(), nil
}

// run adds a partition for each partition directory under the table's
// location that the catalog does not have, and reports each one as MSCK
// REPAIR TABLE does.
func (s *repairTableStmt) run(ctx context.Context, env ddlEnv) (*queryResult, *queryFailure) {
	ref := env.resolve(s.Table)
	t, fail := env.requireTable(ctx, ref)
	if fail != nil {
		return nil, fail
	}
	found, fail := env.partitionDirectories(ctx, t)
	if fail != nil {
		return nil, fail
	}
	existing, err := env.catalog.ListPartitions(ctx, ref.Database, ref.Table)
	if err != nil {
		return nil, catalogFailure(protocol.Wrap(protocol.ErrInternalError, err))
	}
	known := map[string]bool{}
	for _, p := range existing {
		known[partitionPath(t, p.Values)] = true
	}
	res, missing := textResult("result"), []glue.PartitionInput{}
	for _, values := range found {
		path := partitionPath(t, values)
		if known[path] {
			continue
		}
		known[path] = true
		missing = append(missing, partitionInput(t, values, tableLocation(t)+path))
		name := t.Name + ":" + strings.TrimSuffix(path, "/")
		res.Rows = append(res.Rows, textRow("Partitions not in metastore:\t"+name), textRow("Repair: Added partition to metastore "+name))
	}
	if _, aerr := env.writer.CreatePartitions(ctx, ref.Database, ref.Table, missing); aerr != nil {
		return nil, catalogFailure(aerr)
	}
	return res, nil
}

// partitionDirectories lists the table's location and returns the values of
// every partition directory holding an object, in the order found.
func (env ddlEnv) partitionDirectories(ctx context.Context, t glue.Table) ([][]string, *queryFailure) {
	bucket, prefix, ok := splitS3URI(tableLocation(t))
	if !ok || len(t.PartitionKeys) == 0 {
		return nil, nil
	}
	var out [][]string
	token := ""
	for {
		page, aerr := env.list(ctx, bucket, prefix, token, 0)
		if aerr != nil {
			return nil, failure(errorCategoryUser, errorTypeDDLFailed, "FAILED: "+aerr.Code+": "+aerr.Message)
		}
		for _, obj := range page.Objects {
			if values, ok := partitionValuesOf(t, strings.TrimPrefix(obj.Key, prefix)); ok {
				out = append(out, values)
			}
		}
		if token = page.NextContinuationToken; token == "" {
			return out, nil
		}
	}
}

// partitionValuesOf reads a key relative to the table's location as
// key=value/… directories, one per partition key in order.
func partitionValuesOf(t glue.Table, rel string) ([]string, bool) {
	dirs := strings.Split(rel, "/")
	if len(dirs) <= len(t.PartitionKeys) {
		return nil, false
	}
	values := make([]string, len(t.PartitionKeys))
	for i, key := range t.PartitionKeys {
		k, v, ok := strings.Cut(dirs[i], "=")
		if !ok || !strings.EqualFold(k, key.Name) {
			return nil, false
		}
		unescaped, err := url.PathUnescape(v)
		if err != nil {
			return nil, false
		}
		values[i] = unescaped
	}
	return values, true
}

func (s *showPartitionsStmt) run(ctx context.Context, env ddlEnv) (*queryResult, *queryFailure) {
	ref := env.resolve(s.Table)
	t, fail := env.requireTable(ctx, ref)
	if fail != nil {
		return nil, fail
	}
	if len(t.PartitionKeys) == 0 {
		return nil, failure(errorCategoryUser, errorTypeDDLFailed, "FAILED: SemanticException Table "+t.Name+" is not a partitioned table")
	}
	parts, err := env.catalog.ListPartitions(ctx, ref.Database, ref.Table)
	if err != nil {
		return nil, catalogFailure(protocol.Wrap(protocol.ErrInternalError, err))
	}
	names := make([]string, len(parts))
	for i, p := range parts {
		names[i] = strings.TrimSuffix(partitionPath(t, p.Values), "/")
	}
	slices.Sort(names)
	return textResult("partition", names...), nil
}

// ─── Grammar ───────────────────────────────────────────────────

// partitionSpec is one PARTITION (key='value', …) and its LOCATION.
type partitionSpec struct {
	Keys, Values []string
	Location     string
}

// alterPartitionsStmt is ALTER TABLE name ADD [IF NOT EXISTS] PARTITION
// (…) [LOCATION '…'] … or ALTER TABLE name DROP [IF EXISTS] PARTITION (…),
// ….
type alterPartitionsStmt struct {
	Table      tableRef
	Drop       bool
	IfGuard    bool // IF NOT EXISTS on an add, IF EXISTS on a drop
	Partitions []partitionSpec
}

// parseAlterTable reads the partition forms of ALTER TABLE; any other ALTER
// TABLE is the engine's.
func parseAlterTable(p *ddlParser) (ddlStatement, error) {
	table, err := p.tableName()
	if err != nil {
		return nil, err
	}
	s := &alterPartitionsStmt{Table: table}
	switch {
	case p.accept("ADD"):
		s.IfGuard = p.ifNotExists()
	case p.accept("DROP"):
		s.Drop, s.IfGuard = true, p.ifExists()
	default:
		return nil, nil
	}
	if !p.peek().is("PARTITION") {
		return nil, nil
	}
	for p.accept("PARTITION") {
		spec, err := parsePartitionSpec(p, !s.Drop)
		if err != nil {
			return nil, err
		}
		s.Partitions = append(s.Partitions, spec)
		p.acceptSymbol(",")
	}
	return s, p.end()
}

// parsePartitionSpec reads (key='value', …), and on an add its LOCATION.
func parsePartitionSpec(p *ddlParser, withLocation bool) (partitionSpec, error) {
	var spec partitionSpec
	if err := p.expectSymbol("("); err != nil {
		return spec, err
	}
	for !p.acceptSymbol(")") {
		k, err := p.name()
		if err != nil {
			return spec, err
		}
		if err := p.expectSymbol("="); err != nil {
			return spec, err
		}
		v := p.next()
		if v.kind != tokString && v.kind != tokNumber && v.kind != tokWord {
			return spec, p.fail("expected a partition value")
		}
		spec.Keys, spec.Values = append(spec.Keys, k), append(spec.Values, v.text)
		p.acceptSymbol(",")
	}
	if withLocation && p.accept("LOCATION") {
		var err error
		if spec.Location, err = p.str(); err != nil {
			return spec, err
		}
	}
	return spec, nil
}

// repairTableStmt is MSCK REPAIR TABLE name.
type repairTableStmt struct{ Table tableRef }

func parseRepairTable(p *ddlParser) (ddlStatement, error) {
	table, err := p.tableName()
	if err != nil {
		return nil, err
	}
	return &repairTableStmt{Table: table}, p.end()
}
