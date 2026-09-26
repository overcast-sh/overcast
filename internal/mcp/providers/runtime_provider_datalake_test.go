package providers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
)

// The tools' happy paths run against a whole emulator in
// tests/integration/router/mcp_datalake_test.go.

func TestDataLakeTools_validateArgumentsBeforeNeedingTheRouter(t *testing.T) {
	p := NewRuntimeProvider(nil, nil)
	for name, args := range map[string]string{
		"runtime_athena_run_query":         `{"sql":"  "}`,
		"runtime_glue_describe_table":      `{"database":"db"}`,
		"runtime_s3tables_table_snapshots": `{"namespace":"ns","name":"t"}`,
	} {
		fn, ok := p.Handler(name)
		if !ok {
			t.Fatalf("no handler for %s", name)
		}
		if _, err := fn(context.Background(), json.RawMessage(args)); err == nil || !strings.Contains(err.Error(), "required") {
			t.Fatalf("%s(%s) = %v, want a required-argument error", name, args, err)
		}
	}
}

func TestDataLakeTools_needTheRouter(t *testing.T) {
	p := NewRuntimeProvider(nil, nil)
	fn, _ := p.Handler("runtime_glue_describe_table")
	if _, err := fn(context.Background(), json.RawMessage(`{"database":"db","table":"t"}`)); err == nil || !strings.Contains(err.Error(), "router is unavailable") {
		t.Fatalf("err = %v", err)
	}
}

func TestGlueTableFormat(t *testing.T) {
	serde := func(lib, input string) *gluetypes.StorageDescriptor {
		return &gluetypes.StorageDescriptor{SerdeInfo: &gluetypes.SerDeInfo{SerializationLibrary: aws.String(lib)}, InputFormat: aws.String(input)}
	}
	for want, table := range map[string]*gluetypes.Table{
		"ICEBERG": {Parameters: map[string]string{"TABLE_TYPE": "iceberg"}},
		"VIEW":    {TableType: aws.String("VIRTUAL_VIEW")},
		"PARQUET": {StorageDescriptor: serde("org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe", "")},
		"JSON":    {StorageDescriptor: serde("org.openx.data.jsonserde.JsonSerDe", "org.apache.hadoop.mapred.TextInputFormat")},
		"CSV":     {StorageDescriptor: serde("org.apache.hadoop.hive.serde2.lazy.LazySimpleSerDe", "")},
		"ORC":     {Parameters: map[string]string{"classification": "orc"}},
		"":        {},
	} {
		if got := glueTableFormat(table); got != want {
			t.Errorf("glueTableFormat(%+v) = %q, want %q", table, got, want)
		}
	}
}

func TestBoundedSeconds(t *testing.T) {
	if got := boundedSeconds(0, time.Minute, time.Hour); got != time.Minute {
		t.Fatalf("unset = %s", got)
	}
	if got := boundedSeconds(7200, time.Minute, time.Hour); got != time.Hour {
		t.Fatalf("over the limit = %s", got)
	}
}
