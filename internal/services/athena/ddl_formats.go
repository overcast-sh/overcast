package athena

import (
	"strings"

	"github.com/overcast-sh/overcast/internal/services/glue"
)

// ddl_formats.go — the Hive storage formats a CREATE EXTERNAL TABLE names,
// as the input format, output format and SerDe classes Glue records.

const (
	lazySimpleSerDe  = "org.apache.hadoop.hive.serde2.lazy.LazySimpleSerDe"
	textInputFormat  = "org.apache.hadoop.mapred.TextInputFormat"
	textOutputFormat = "org.apache.hadoop.hive.ql.io.HiveIgnoreKeyTextOutputFormat"
)

// hiveFormat is one STORED AS format's classes.
type hiveFormat struct{ InputFormat, OutputFormat, SerDe string }

// hiveFormats are the formats STORED AS accepts by name.
var hiveFormats = map[string]hiveFormat{
	"textfile": {textInputFormat, textOutputFormat, lazySimpleSerDe},
	"parquet": {"org.apache.hadoop.hive.ql.io.parquet.MapredParquetInputFormat",
		"org.apache.hadoop.hive.ql.io.parquet.MapredParquetOutputFormat",
		"org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe"},
	"orc": {"org.apache.hadoop.hive.ql.io.orc.OrcInputFormat",
		"org.apache.hadoop.hive.ql.io.orc.OrcOutputFormat",
		"org.apache.hadoop.hive.ql.io.orc.OrcSerde"},
	"avro": {"org.apache.hadoop.hive.ql.io.avro.AvroContainerInputFormat",
		"org.apache.hadoop.hive.ql.io.avro.AvroContainerOutputFormat",
		"org.apache.hadoop.hive.serde2.avro.AvroSerDe"},
	"sequencefile": {"org.apache.hadoop.mapred.SequenceFileInputFormat",
		"org.apache.hadoop.hive.ql.io.HiveSequenceFileOutputFormat", lazySimpleSerDe},
	"rcfile": {"org.apache.hadoop.hive.ql.io.RCFileInputFormat",
		"org.apache.hadoop.hive.ql.io.RCFileOutputFormat",
		"org.apache.hadoop.hive.serde2.columnar.LazyBinaryColumnarSerDe"},
}

// storageDescriptor is where and how the table's data is stored: the
// columns, the location, the formats STORED AS names (text when it names
// none) and the SerDe ROW FORMAT names (the format's own when it names none).
func (s *createTableStmt) storageDescriptor() (*glue.StorageDescriptor, bool) {
	format, known := hiveFormats["textfile"], true
	switch {
	case s.StoredAs.InputFormat != "":
		format = hiveFormat{InputFormat: s.StoredAs.InputFormat, OutputFormat: s.StoredAs.OutputFormat}
	case s.StoredAs.Format != "":
		format, known = hiveFormats[strings.ToLower(s.StoredAs.Format)]
	}
	serde := &glue.SerDeInfo{SerializationLibrary: format.SerDe, Parameters: map[string]string{}}
	if s.RowFormat != nil {
		serde.SerializationLibrary, serde.Parameters = s.RowFormat.SerDe, s.RowFormat.Params
	}
	sd := &glue.StorageDescriptor{
		Columns:       glueColumns(s.Columns),
		Location:      s.Location,
		InputFormat:   format.InputFormat,
		OutputFormat:  format.OutputFormat,
		SerdeInfo:     serde,
		BucketColumns: s.BucketBy,
	}
	if s.Buckets > 0 {
		sd.NumberOfBuckets = &s.Buckets
	}
	return sd, known
}

func glueColumns(cols []columnDef) []glue.Column {
	out := make([]glue.Column, len(cols))
	for i, c := range cols {
		out[i] = glue.Column{Name: c.Name, Type: c.Type, Comment: c.Comment}
	}
	return out
}
