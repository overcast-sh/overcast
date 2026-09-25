package glue

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func stringStats(column string) ColumnStatistics {
	return ColumnStatistics{ColumnName: column, ColumnType: "string", AnalyzedTime: 1,
		StatisticsData: exactJSON{"Type": "STRING", "StringColumnStatisticsData": map[string]any{"NumberOfNulls": float64(0)}}}
}

func TestColumnStatistics_roundTripForTableAndPartition(t *testing.T) {
	// Given: a partitioned table with one partition
	ctx := context.Background()
	s, _, _ := newTestService(t)
	seedTable(t, s, "db", "t")
	_, aerr := s.createPartitionTyped(ctx, &createPartitionReq{DatabaseName: "db", TableName: "t",
		PartitionInput: &PartitionInput{Values: []string{"2026", "09"}}})
	mustOK(t, "CreatePartition", aerr)
	table := columnStatisticsTarget{DatabaseName: "db", TableName: "t"}
	part := columnStatisticsTarget{DatabaseName: "db", TableName: "t", PartitionValues: []string{"2026", "09"}}

	// When: statistics are written for a real column and an unknown one
	up, aerr := s.updateColumnStatisticsForTableTyped(ctx, &updateColumnStatisticsReq{columnStatisticsTarget: table,
		ColumnStatisticsList: []ColumnStatistics{stringStats("ID"), stringStats("nope")}})
	mustOK(t, "UpdateColumnStatisticsForTable", aerr)
	_, aerr = s.updateColumnStatisticsForPartitionTyped(ctx, &updateColumnStatisticsReq{columnStatisticsTarget: part,
		ColumnStatisticsList: []ColumnStatistics{stringStats("id")}})
	mustOK(t, "UpdateColumnStatisticsForPartition", aerr)

	// Then: the unknown column is refused in Errors and the rest reads back
	if len(up.Errors) != 1 || up.Errors[0].ColumnStatistics.ColumnName != "nope" {
		t.Fatalf("Errors = %+v", up.Errors)
	}
	got, aerr := s.getColumnStatisticsForTableTyped(ctx, &getColumnStatisticsReq{columnStatisticsTarget: table, ColumnNames: []string{"id", "year"}})
	mustOK(t, "GetColumnStatisticsForTable", aerr)
	if len(got.ColumnStatisticsList) != 1 || got.ColumnStatisticsList[0].ColumnName != "ID" || len(got.Errors) != 1 || got.Errors[0].ColumnName != "year" {
		t.Fatalf("GetColumnStatisticsForTable = %+v", got)
	}

	// When: the partition is deleted, and the table's statistics are deleted
	_, aerr = s.deletePartitionTyped(ctx, &deletePartitionReq{DatabaseName: "db", TableName: "t", PartitionValues: []string{"2026", "09"}})
	mustOK(t, "DeletePartition", aerr)
	_, aerr = s.deleteColumnStatisticsForTableTyped(ctx, &deleteColumnStatisticsReq{columnStatisticsTarget: table, ColumnName: "id"})
	mustOK(t, "DeleteColumnStatisticsForTable", aerr)

	// Then: nothing is left, and the partition's statistics went with it
	_, aerr = s.getColumnStatisticsForPartitionTyped(ctx, &getColumnStatisticsReq{columnStatisticsTarget: part, ColumnNames: []string{"id"}})
	wantCode(t, "GetColumnStatisticsForPartition on a deleted partition", aerr, codeEntityNotFound)
	_, aerr = s.deleteColumnStatisticsForTableTyped(ctx, &deleteColumnStatisticsReq{columnStatisticsTarget: table, ColumnName: "id"})
	wantCode(t, "DeleteColumnStatisticsForTable twice", aerr, codeEntityNotFound)
	pairs, err := s.store.store.Scan(ctx, nsColumnStatistics, "")
	if err != nil || len(pairs) != 0 {
		t.Fatalf("left behind: %+v, %v", pairs, err)
	}
}

func TestColumnStatistics_goWithTheirTable(t *testing.T) {
	// Given: a table with statistics
	ctx := context.Background()
	s, _, _ := newTestService(t)
	seedTable(t, s, "db", "t")
	_, aerr := s.updateColumnStatisticsForTableTyped(ctx, &updateColumnStatisticsReq{
		columnStatisticsTarget: columnStatisticsTarget{DatabaseName: "db", TableName: "t"},
		ColumnStatisticsList:   []ColumnStatistics{stringStats("id")}})
	mustOK(t, "UpdateColumnStatisticsForTable", aerr)

	// When: the table is dropped and recreated
	_, aerr = s.deleteTableTyped(ctx, &deleteTableReq{DatabaseName: "db", Name: "t"})
	mustOK(t, "DeleteTable", aerr)
	seedTable(t, s, "db", "t")

	// Then: the new table starts without the old one's statistics
	got, aerr := s.getColumnStatisticsForTableTyped(ctx, &getColumnStatisticsReq{
		columnStatisticsTarget: columnStatisticsTarget{DatabaseName: "db", TableName: "t"}, ColumnNames: []string{"id"}})
	mustOK(t, "GetColumnStatisticsForTable", aerr)
	if len(got.ColumnStatisticsList) != 0 {
		t.Fatalf("stale statistics survived: %+v", got.ColumnStatisticsList)
	}
}

func TestColumnStatistics_bigintBoundsRoundTripExactly(t *testing.T) {
	// Given: statistics whose maximum is the largest bigint, as a client sends it
	ctx := context.Background()
	s, _, _ := newTestService(t)
	seedTable(t, s, "db", "t")
	var in updateColumnStatisticsReq
	body := `{"DatabaseName":"db","TableName":"t","ColumnStatisticsList":[{"ColumnName":"id","ColumnType":"bigint",
		"StatisticsData":{"Type":"LONG","LongColumnStatisticsData":{"MaximumValue":9223372036854775807,"NumberOfNulls":0,"NumberOfDistinctValues":1}}}]}`
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatal(err)
	}

	// When: they are stored and read back
	_, aerr := s.updateColumnStatisticsForTableTyped(ctx, &in)
	mustOK(t, "UpdateColumnStatisticsForTable", aerr)
	got, aerr := s.getColumnStatisticsForTableTyped(ctx, &getColumnStatisticsReq{
		columnStatisticsTarget: columnStatisticsTarget{DatabaseName: "db", TableName: "t"}, ColumnNames: []string{"id"}})
	mustOK(t, "GetColumnStatisticsForTable", aerr)

	// Then: the maximum is the number that was written, not a float's rounding
	out, err := json.Marshal(got.ColumnStatisticsList[0].StatisticsData)
	if err != nil || !strings.Contains(string(out), `"MaximumValue":9223372036854775807`) {
		t.Fatalf("StatisticsData = %s, %v", out, err)
	}
}
