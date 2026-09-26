package glue

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Column statistics, stored and echoed. Trino's Glue metastore writes them
// after every CREATE TABLE AS and INSERT into a Hive table, and reads them
// back when it plans, so a write that is refused fails the whole query.
// Overcast computes none itself: what it returns is what a client wrote.

// List limits from the model: UpdateColumnStatisticsList holds at most 25
// entries and GetColumnNamesList at most 100.
const (
	maxColumnStatisticsUpdates = 25
	maxColumnStatisticsNames   = 100
)

// ColumnStatistics is one column's statistics. StatisticsData is kept as
// decoded JSON: Overcast never reads its per-type members.
type ColumnStatistics struct {
	ColumnName     string    `json:"ColumnName"`
	ColumnType     string    `json:"ColumnType"`
	AnalyzedTime   float64   `json:"AnalyzedTime"`
	StatisticsData exactJSON `json:"StatisticsData"`
}

// exactJSON is a JSON object whose numbers are kept as written, so a
// bigint's minimum or maximum above 2^53 round-trips rather than being
// rounded through a float64.
type exactJSON map[string]any

func (e *exactJSON) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return err
	}
	*e = m
	return nil
}

// ColumnStatisticsError is an Update*'s refusal of one entry.
type ColumnStatisticsError struct {
	ColumnStatistics *ColumnStatistics `json:"ColumnStatistics,omitempty"`
	Error            *ErrorDetail      `json:"Error,omitempty"`
}

// ColumnError is a Get*'s refusal of one column name.
type ColumnError struct {
	ColumnName string       `json:"ColumnName,omitempty"`
	Error      *ErrorDetail `json:"Error,omitempty"`
}

// columnStatisticsTarget names a table, or one partition of it when
// PartitionValues is set, whose statistics a request reads or writes.
type columnStatisticsTarget struct {
	catalogRef
	DatabaseName    string   `json:"DatabaseName" cbor:"DatabaseName"`
	TableName       string   `json:"TableName" cbor:"TableName"`
	PartitionValues []string `json:"PartitionValues" cbor:"PartitionValues"`
}

type updateColumnStatisticsReq struct {
	columnStatisticsTarget
	ColumnStatisticsList []ColumnStatistics `json:"ColumnStatisticsList" cbor:"ColumnStatisticsList"`
}

type updateColumnStatisticsResp struct {
	Errors []ColumnStatisticsError `json:"Errors" cbor:"Errors"`
}

type getColumnStatisticsReq struct {
	columnStatisticsTarget
	ColumnNames []string `json:"ColumnNames" cbor:"ColumnNames"`
}

type getColumnStatisticsResp struct {
	ColumnStatisticsList []ColumnStatistics `json:"ColumnStatisticsList" cbor:"ColumnStatisticsList"`
	Errors               []ColumnError      `json:"Errors" cbor:"Errors"`
}

type deleteColumnStatisticsReq struct {
	columnStatisticsTarget
	ColumnName string `json:"ColumnName" cbor:"ColumnName"`
}

// tableStatisticsPrefix and partitionStatisticsPrefix are where a table's
// and a partition's statistics live in nsColumnStatistics; see store.go.
func tableStatisticsPrefix(dbName, tableName string) string {
	return tablePrefix(dbName, tableName) + "t/"
}

func partitionStatisticsPrefix(dbName, tableName string, values []string) string {
	escaped := make([]string, len(values))
	for i, v := range values {
		escaped[i] = esc(v)
	}
	return tablePrefix(dbName, tableName) + "p/" + esc(strings.Join(escaped, "/")) + "/"
}

// statisticsPrefix is the key prefix t's statistics live under, or those of
// its partition values when partition is set, which must exist.
func (s *Service) statisticsPrefix(ctx context.Context, t *tableRecord, values []string, partition bool) (string, *protocol.AWSError) {
	if !partition {
		return tableStatisticsPrefix(t.DatabaseName, t.Name), nil
	}
	if aerr := checkPartitionValues(t, values); aerr != nil {
		return "", aerr
	}
	_, found, err := s.store.getPartition(ctx, t.DatabaseName, t.Name, values)
	if err != nil {
		return "", errInternal(err)
	}
	if !found {
		return "", errPartitionNotFound()
	}
	return partitionStatisticsPrefix(t.DatabaseName, t.Name, values), nil
}

// hasColumn reports whether name is one of the table's columns or partition
// keys, compared as Glue folds names.
func hasColumn(t *tableRecord, name string) bool {
	match := func(c Column) bool { return strings.EqualFold(c.Name, name) }
	if t.StorageDescriptor != nil && slices.ContainsFunc(t.StorageDescriptor.Columns, match) {
		return true
	}
	return slices.ContainsFunc(t.PartitionKeys, match)
}

func (s *Service) updateColumnStatistics(ctx context.Context, req *updateColumnStatisticsReq, partition bool) (*updateColumnStatisticsResp, *protocol.AWSError) {
	if n := len(req.ColumnStatisticsList); n == 0 || n > maxColumnStatisticsUpdates {
		return nil, errInvalidInput("ColumnStatisticsList must hold between 1 and %d entries.", maxColumnStatisticsUpdates)
	}
	t, unlock, aerr := s.lockTable(ctx, req.DatabaseName, req.TableName)
	if aerr != nil {
		return nil, aerr
	}
	defer unlock()
	prefix, aerr := s.statisticsPrefix(ctx, t, req.PartitionValues, partition)
	if aerr != nil {
		return nil, aerr
	}
	resp := &updateColumnStatisticsResp{Errors: []ColumnStatisticsError{}}
	for i := range req.ColumnStatisticsList {
		cs := req.ColumnStatisticsList[i]
		if !hasColumn(t, cs.ColumnName) {
			resp.Errors = append(resp.Errors, ColumnStatisticsError{ColumnStatistics: &cs,
				Error: errorDetail(errInvalidInput("Column %s is not a column of table %s.", cs.ColumnName, t.Name))})
			continue
		}
		if err := s.store.put(ctx, nsColumnStatistics, prefix+esc(normName(cs.ColumnName)), &cs); err != nil {
			return nil, errInternal(err)
		}
	}
	return resp, nil
}

func (s *Service) getColumnStatistics(ctx context.Context, req *getColumnStatisticsReq, partition bool) (*getColumnStatisticsResp, *protocol.AWSError) {
	if n := len(req.ColumnNames); n == 0 || n > maxColumnStatisticsNames {
		return nil, errInvalidInput("ColumnNames must hold between 1 and %d names.", maxColumnStatisticsNames)
	}
	t, aerr := s.requireTable(ctx, normName(req.DatabaseName), normName(req.TableName))
	if aerr != nil {
		return nil, aerr
	}
	prefix, aerr := s.statisticsPrefix(ctx, t, req.PartitionValues, partition)
	if aerr != nil {
		return nil, aerr
	}
	resp := &getColumnStatisticsResp{ColumnStatisticsList: []ColumnStatistics{}, Errors: []ColumnError{}}
	for _, name := range req.ColumnNames {
		var cs ColumnStatistics
		found, err := s.store.get(ctx, nsColumnStatistics, prefix+esc(normName(name)), &cs)
		if err != nil {
			return nil, errInternal(err)
		}
		if !found {
			resp.Errors = append(resp.Errors, ColumnError{ColumnName: name,
				Error: errorDetail(glueError(codeEntityNotFound, "No statistics found for column %s.", name))})
			continue
		}
		resp.ColumnStatisticsList = append(resp.ColumnStatisticsList, cs)
	}
	return resp, nil
}

func (s *Service) deleteColumnStatistics(ctx context.Context, req *deleteColumnStatisticsReq, partition bool) (*struct{}, *protocol.AWSError) {
	if req.ColumnName == "" {
		return nil, errInvalidInput("ColumnName is required.")
	}
	t, unlock, aerr := s.lockTable(ctx, req.DatabaseName, req.TableName)
	if aerr != nil {
		return nil, aerr
	}
	defer unlock()
	prefix, aerr := s.statisticsPrefix(ctx, t, req.PartitionValues, partition)
	if aerr != nil {
		return nil, aerr
	}
	// Presence rather than a decode, so a corrupt record can still be deleted.
	key := prefix + esc(normName(req.ColumnName))
	_, found, err := s.store.store.Get(ctx, nsColumnStatistics, key)
	if err != nil {
		return nil, errInternal(err)
	}
	if !found {
		return nil, glueError(codeEntityNotFound, "No statistics found for column %s.", req.ColumnName)
	}
	if err := s.store.store.Delete(ctx, nsColumnStatistics, key); err != nil {
		return nil, errInternal(err)
	}
	return &struct{}{}, nil
}

func (s *Service) updateColumnStatisticsForTableTyped(ctx context.Context, req *updateColumnStatisticsReq) (*updateColumnStatisticsResp, *protocol.AWSError) {
	return s.updateColumnStatistics(ctx, req, false)
}

func (s *Service) updateColumnStatisticsForPartitionTyped(ctx context.Context, req *updateColumnStatisticsReq) (*updateColumnStatisticsResp, *protocol.AWSError) {
	return s.updateColumnStatistics(ctx, req, true)
}

func (s *Service) getColumnStatisticsForTableTyped(ctx context.Context, req *getColumnStatisticsReq) (*getColumnStatisticsResp, *protocol.AWSError) {
	return s.getColumnStatistics(ctx, req, false)
}

func (s *Service) getColumnStatisticsForPartitionTyped(ctx context.Context, req *getColumnStatisticsReq) (*getColumnStatisticsResp, *protocol.AWSError) {
	return s.getColumnStatistics(ctx, req, true)
}

func (s *Service) deleteColumnStatisticsForTableTyped(ctx context.Context, req *deleteColumnStatisticsReq) (*struct{}, *protocol.AWSError) {
	return s.deleteColumnStatistics(ctx, req, false)
}

func (s *Service) deleteColumnStatisticsForPartitionTyped(ctx context.Context, req *deleteColumnStatisticsReq) (*struct{}, *protocol.AWSError) {
	return s.deleteColumnStatistics(ctx, req, true)
}
