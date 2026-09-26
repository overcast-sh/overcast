package providers

// runtime_provider_catalog.go — the handlers behind runtime_glue_describe_table
// and runtime_s3tables_table_snapshots (declared in
// runtime_provider_datalake.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"

	"github.com/overcast-sh/overcast/internal/icebergmeta"
	"github.com/overcast-sh/overcast/internal/sdkconfig"
)

func (p *RuntimeProvider) toolGlueDescribeTable(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Database string `json:"database"`
		Table    string `json:"table"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	database, table := strings.TrimSpace(args.Database), strings.TrimSpace(args.Table)
	if database == "" || table == "" {
		return nil, fmt.Errorf("database and table are required")
	}
	cfg, err := p.sdkConfig()
	if err != nil {
		return nil, err
	}
	client := glue.NewFromConfig(cfg)
	out, err := client.GetTable(ctx, &glue.GetTableInput{DatabaseName: aws.String(database), Name: aws.String(table)})
	if err != nil {
		return nil, fmt.Errorf("get table %s.%s: %w", database, table, err)
	}
	partitions, err := countPartitions(ctx, client, database, table)
	if err != nil {
		return nil, err
	}
	t := out.Table
	result := map[string]any{
		"database":        database,
		"table":           aws.ToString(t.Name),
		"table_type":      aws.ToString(t.TableType),
		"format":          glueTableFormat(t),
		"columns":         glueColumns(nil),
		"partition_keys":  glueColumns(t.PartitionKeys),
		"partition_count": partitions,
		"parameters":      t.Parameters,
	}
	if sd := t.StorageDescriptor; sd != nil {
		result["location"] = aws.ToString(sd.Location)
		result["columns"] = glueColumns(sd.Columns)
	}
	return result, nil
}

func countPartitions(ctx context.Context, client *glue.Client, database, table string) (int, error) {
	n := 0
	pages := glue.NewGetPartitionsPaginator(client, &glue.GetPartitionsInput{DatabaseName: aws.String(database), TableName: aws.String(table)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return 0, fmt.Errorf("get partitions of %s.%s: %w", database, table, err)
		}
		n += len(page.Partitions)
	}
	return n, nil
}

func glueColumns(cols []gluetypes.Column) []map[string]any {
	out := make([]map[string]any, len(cols))
	for i, c := range cols {
		out[i] = map[string]any{"name": aws.ToString(c.Name), "type": aws.ToString(c.Type)}
		if c.Comment != nil {
			out[i]["comment"] = *c.Comment
		}
	}
	return out
}

// glueFormatMarkers are matched against a table's SerDe, input format and
// crawler classification, in that order — the same reading as the console's
// table-format.ts.
var glueFormatMarkers = []struct {
	format string
	marker *regexp.Regexp
}{
	{"PARQUET", regexp.MustCompile(`(?i)parquet`)},
	{"ORC", regexp.MustCompile(`(?i)^orc$|\.orc\.|orcserde|orcinputformat`)},
	{"AVRO", regexp.MustCompile(`(?i)avro`)},
	{"JSON", regexp.MustCompile(`(?i)json`)},
	{"CSV", regexp.MustCompile(`(?i)csv|lazysimpleserde|textinputformat`)},
}

// glueTableFormat is what a Glue table holds, or "" when nothing on it says.
func glueTableFormat(t *gluetypes.Table) string {
	if strings.EqualFold(glueParameter(t.Parameters, "table_type"), "ICEBERG") {
		return "ICEBERG"
	}
	if aws.ToString(t.TableType) == "VIRTUAL_VIEW" {
		return "VIEW"
	}
	var clues []string
	if sd := t.StorageDescriptor; sd != nil {
		if sd.SerdeInfo != nil {
			clues = append(clues, aws.ToString(sd.SerdeInfo.SerializationLibrary))
		}
		clues = append(clues, aws.ToString(sd.InputFormat))
	}
	clues = append(clues, glueParameter(t.Parameters, "classification"))
	for _, clue := range clues {
		for _, m := range glueFormatMarkers {
			if clue != "" && m.marker.MatchString(clue) {
				return m.format
			}
		}
	}
	return ""
}

// glueParameter is a table parameter by name, ignoring case: Athena writes
// table_type, PyIceberg TABLE_TYPE.
func glueParameter(params map[string]string, name string) string {
	for k, v := range params {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func (p *RuntimeProvider) toolS3TablesTableSnapshots(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		TableBucketARN string `json:"table_bucket_arn"`
		Namespace      string `json:"namespace"`
		Name           string `json:"name"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.TableBucketARN) == "" || strings.TrimSpace(args.Namespace) == "" || strings.TrimSpace(args.Name) == "" {
		return nil, fmt.Errorf("table_bucket_arn, namespace and name are required")
	}
	cfg, err := p.sdkConfig()
	if err != nil {
		return nil, err
	}
	loc, err := s3tables.NewFromConfig(cfg).GetTableMetadataLocation(ctx, &s3tables.GetTableMetadataLocationInput{
		TableBucketARN: aws.String(args.TableBucketARN), Namespace: aws.String(args.Namespace), Name: aws.String(args.Name),
	})
	if err != nil {
		return nil, fmt.Errorf("get table metadata location: %w", err)
	}
	location := aws.ToString(loc.MetadataLocation)
	if location == "" { // created without a schema: nothing has committed to it
		return map[string]any{"snapshots": []any{}}, nil
	}
	meta, err := readIcebergMetadata(ctx, s3.NewFromConfig(cfg, sdkconfig.PathStyle), location)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"metadata_location": location,
		"format_version":    meta.FormatVersion,
		"snapshots":         icebergSnapshots(meta),
	}
	if meta.CurrentSnapshotID >= 0 {
		out["current_snapshot_id"] = meta.CurrentSnapshotID
	}
	return out, nil
}

// readIcebergMetadata reads and parses the metadata file at an s3:// URI.
func readIcebergMetadata(ctx context.Context, client *s3.Client, uri string) (*icebergmeta.Metadata, error) {
	bucket, key, ok := strings.Cut(strings.TrimPrefix(uri, "s3://"), "/")
	if !ok || !strings.HasPrefix(uri, "s3://") {
		return nil, fmt.Errorf("metadata location %q is not an s3:// URI", uri)
	}
	obj, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", uri, err)
	}
	defer obj.Body.Close()
	raw, err := io.ReadAll(obj.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", uri, err)
	}
	meta, err := icebergmeta.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", uri, err)
	}
	return meta, nil
}

// icebergSnapshots are the table's snapshots in the order the metadata
// lists them, which is the order they were committed.
func icebergSnapshots(meta *icebergmeta.Metadata) []map[string]any {
	out := make([]map[string]any, len(meta.Snapshots))
	for i, sn := range meta.Snapshots {
		s := map[string]any{
			"snapshot_id": sn.SnapshotID,
			"timestamp":   time.UnixMilli(sn.TimestampMS).UTC().Format(time.RFC3339Nano),
			"operation":   sn.Summary["operation"],
			"summary":     sn.Summary,
			"current":     sn.SnapshotID == meta.CurrentSnapshotID,
		}
		if sn.ParentSnapshotID != nil {
			s["parent_snapshot_id"] = *sn.ParentSnapshotID
		}
		if sn.SequenceNumber > 0 {
			s["sequence_number"] = sn.SequenceNumber
		}
		if sn.ManifestList != "" {
			s["manifest_list"] = sn.ManifestList
		}
		out[i] = s
	}
	return out
}
