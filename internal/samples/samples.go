// Package samples loads the sample datasets: files in an S3 bucket, a Glue
// database and tables over them, and, when the Athena engine is running, an
// Iceberg copy made with CREATE TABLE AS. `overcast samples load` runs it
// against an endpoint, and POST /_overcast/samples/{name} runs it in process
// for the console; both drive Overcast through its AWS API.
//
// Loading is idempotent: objects are rewritten byte for byte, the DDL says
// IF NOT EXISTS, and the Iceberg copy is made only when it is missing.
package samples

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/overcast-sh/overcast/internal/athenaquery"
	"github.com/overcast-sh/overcast/internal/sdkconfig"
	athenasvc "github.com/overcast-sh/overcast/internal/services/athena"
)

// icebergTimeout bounds the wait for the Iceberg copy's CTAS on a running
// engine; the copy is a few hundred rows.
const icebergTimeout = 2 * time.Minute

// ErrUnknownDataset is returned for a dataset name Names does not list.
var ErrUnknownDataset = errors.New("unknown sample dataset")

// RequiredServices are the services a load goes through.
var RequiredServices = []string{"athena", "glue", "s3"}

// datasets are the loadable datasets, by name.
var datasets = map[string]func() dataset{"analytics": analyticsDataset}

// Names lists the datasets, sorted.
func Names() []string {
	names := make([]string, 0, len(datasets))
	for name := range datasets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// dataset is what loading one creates.
type dataset struct {
	Name, Bucket, Database string
	Objects                []object
	// Statements are the DDL that describes the objects, run in order.
	Statements []string
	Tables     []Table
	// Iceberg is the copy IcebergSQL makes when the engine is running.
	Iceberg    *Table
	IcebergSQL string
}

type object struct {
	Key         string
	Body        []byte
	ContentType string
}

// Report is what a load left in place.
type Report struct {
	Dataset  string  `json:"dataset"`
	Bucket   string  `json:"bucket"`
	Database string  `json:"database"`
	Tables   []Table `json:"tables"`
	// IcebergSkipped says why there is no Iceberg copy, when there is none.
	IcebergSkipped string `json:"icebergSkipped,omitempty"`
}

// Table is one table the dataset has.
type Table struct {
	Name       string `json:"name"`
	Format     string `json:"format"`
	Location   string `json:"location"`
	Partitions int    `json:"partitions,omitempty"`
	Rows       int    `json:"rows"`
}

// S3API, GlueAPI and the athenaquery.API are the calls a load makes.
type (
	S3API interface {
		CreateBucket(context.Context, *s3.CreateBucketInput, ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
		PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	}
	GlueAPI interface {
		GetTable(context.Context, *glue.GetTableInput, ...func(*glue.Options)) (*glue.GetTableOutput, error)
	}
)

// EngineFunc reports the Athena query engine's status.
type EngineFunc func(context.Context) (athenasvc.EngineStatus, error)

// Loader loads datasets through the AWS API.
type Loader struct {
	S3     S3API
	Glue   GlueAPI
	Athena athenaquery.API
	Engine EngineFunc
	// Region is where the bucket is created.
	Region string
	// Progress, when set, hears each step as it starts.
	Progress func(step string)
}

// NewLoader is a Loader whose clients use cfg.
func NewLoader(cfg aws.Config, engine EngineFunc) *Loader {
	return &Loader{
		S3:     s3.NewFromConfig(cfg, sdkconfig.PathStyle),
		Glue:   glue.NewFromConfig(cfg),
		Athena: athena.NewFromConfig(cfg),
		Engine: engine,
		Region: cfg.Region,
	}
}

// Load loads the dataset called name.
func (l *Loader) Load(ctx context.Context, name string) (*Report, error) {
	newDataset, ok := datasets[name]
	if !ok {
		return nil, fmt.Errorf("%w %q: the datasets are %v", ErrUnknownDataset, name, Names())
	}
	ds := newDataset()
	if err := l.writeObjects(ctx, ds); err != nil {
		return nil, err
	}
	for _, sql := range ds.Statements {
		if err := l.query(ctx, ds, sql, 0); err != nil {
			return nil, err
		}
	}
	report := &Report{Dataset: ds.Name, Bucket: ds.Bucket, Database: ds.Database, Tables: slices.Clone(ds.Tables)}
	if ds.Iceberg != nil {
		if err := l.loadIceberg(ctx, ds, report); err != nil {
			return nil, err
		}
	}
	return report, nil
}

func (l *Loader) step(format string, args ...any) {
	if l.Progress != nil {
		l.Progress(fmt.Sprintf(format, args...))
	}
}

func (l *Loader) writeObjects(ctx context.Context, ds dataset) error {
	l.step("creating bucket %s", ds.Bucket)
	if _, err := l.S3.CreateBucket(ctx, createBucketInput(ds.Bucket, l.Region)); err != nil && !bucketIsOurs(err) {
		return fmt.Errorf("create bucket %s: %w", ds.Bucket, err)
	}
	l.step("writing %d objects", len(ds.Objects))
	for _, o := range ds.Objects {
		if _, err := l.S3.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(ds.Bucket), Key: aws.String(o.Key), Body: bytes.NewReader(o.Body), ContentType: aws.String(o.ContentType),
		}); err != nil {
			return fmt.Errorf("put s3://%s/%s: %w", ds.Bucket, o.Key, err)
		}
	}
	return nil
}

// createBucketInput creates bucket in region, which outside us-east-1 S3
// wants named as the location constraint.
func createBucketInput(bucket, region string) *s3.CreateBucketInput {
	in := &s3.CreateBucketInput{Bucket: aws.String(bucket)}
	if region != "" && region != "us-east-1" {
		in.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(region)}
	}
	return in
}

// bucketIsOurs reports whether a CreateBucket failed only because this
// account already owns the bucket, which a reload expects.
func bucketIsOurs(err error) bool {
	var owned *s3types.BucketAlreadyOwnedByYou
	return errors.As(err, &owned)
}

// resultsLocation is where a dataset's own queries keep their results: a
// location in its bucket, which a query of the dataset can use too.
func resultsLocation(ds dataset) string { return s3URI(ds.Bucket, "results/") }

// query runs sql to completion, results kept under the dataset's bucket.
func (l *Loader) query(ctx context.Context, ds dataset, sql string, timeout time.Duration) error {
	l.step("running %s", firstLine(sql))
	res, err := athenaquery.Run(ctx, l.Athena, athenaquery.Request{
		SQL: sql, OutputLocation: resultsLocation(ds),
	}, athenaquery.Options{Timeout: timeout})
	if err != nil {
		return err
	}
	if res.TimedOut {
		return fmt.Errorf("query %s did not finish within %s", res.QueryExecutionID, timeout)
	}
	return res.Failure()
}

// loadIceberg makes the Iceberg copy when the engine is ready and the copy
// does not exist, and otherwise says in report why there is none.
func (l *Loader) loadIceberg(ctx context.Context, ds dataset, report *Report) error {
	exists, err := l.tableExists(ctx, ds.Database, ds.Iceberg.Name)
	if err != nil {
		return err
	}
	if !exists {
		if reason := l.engineNotReady(ctx); reason != "" {
			report.IcebergSkipped = reason
			return nil
		}
		if err := l.query(ctx, ds, ds.IcebergSQL, icebergTimeout); err != nil {
			return fmt.Errorf("create the Iceberg copy: %w", err)
		}
	}
	report.Tables = append(report.Tables, *ds.Iceberg)
	return nil
}

func (l *Loader) tableExists(ctx context.Context, database, table string) (bool, error) {
	_, err := l.Glue.GetTable(ctx, &glue.GetTableInput{DatabaseName: aws.String(database), Name: aws.String(table)})
	var notFound *gluetypes.EntityNotFoundException
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &notFound):
		return false, nil
	}
	return false, fmt.Errorf("get table %s.%s: %w", database, table, err)
}

// engineNotReady is why the engine cannot make the Iceberg copy now, or "".
func (l *Loader) engineNotReady(ctx context.Context) string {
	if l.Engine == nil {
		return "the Athena engine status is unknown."
	}
	st, err := l.Engine(ctx)
	if err != nil {
		return "the Athena engine status is unavailable: " + err.Error() + "."
	}
	switch st.State {
	case athenasvc.EngineReady:
		return ""
	case athenasvc.EngineOff:
		return "the Athena engine is off: " + st.Reason
	}
	return fmt.Sprintf("the Athena engine is %s; once a query has started it, load the dataset again for the Iceberg copy.", st.State)
}

// firstLine is sql's first line, to name a statement in progress.
func firstLine(sql string) string {
	line, _, _ := strings.Cut(sql, "\n")
	return line
}
