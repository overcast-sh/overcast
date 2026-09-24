package firehose

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

type createDeliveryStreamReq struct {
	DeliveryStreamName string                `json:"DeliveryStreamName" cbor:"DeliveryStreamName"`
	DeliveryStreamType string                `json:"DeliveryStreamType" cbor:"DeliveryStreamType"`
	Tags               []serviceutil.TagPair `json:"Tags" cbor:"Tags"`

	// Destination configurations, the source configuration and the
	// encryption configuration are carried as raw JSON rather than typed:
	// Overcast stores and echoes each verbatim on DescribeDeliveryStream (see
	// DeliveryStream.Destinations in service.go) without acting on any field
	// within them, so there is nothing here for a typed struct to read.
	// json.RawMessage has no CBOR encoding of its own, so a
	// CreateDeliveryStream carrying one of these over RPC v2 CBOR — a path
	// CloudFormation itself never takes — fails to decode rather than
	// silently keeping the property; `cbor:"-"` states that rather than
	// leaving it to a confusing runtime error.
	S3DestinationConfiguration                      json.RawMessage `json:"S3DestinationConfiguration,omitempty" cbor:"-"`
	ExtendedS3DestinationConfiguration              json.RawMessage `json:"ExtendedS3DestinationConfiguration,omitempty" cbor:"-"`
	RedshiftDestinationConfiguration                json.RawMessage `json:"RedshiftDestinationConfiguration,omitempty" cbor:"-"`
	ElasticsearchDestinationConfiguration           json.RawMessage `json:"ElasticsearchDestinationConfiguration,omitempty" cbor:"-"`
	AmazonopensearchserviceDestinationConfiguration json.RawMessage `json:"AmazonopensearchserviceDestinationConfiguration,omitempty" cbor:"-"`
	HttpEndpointDestinationConfiguration            json.RawMessage `json:"HttpEndpointDestinationConfiguration,omitempty" cbor:"-"`
	SplunkDestinationConfiguration                  json.RawMessage `json:"SplunkDestinationConfiguration,omitempty" cbor:"-"`
	SnowflakeDestinationConfiguration               json.RawMessage `json:"SnowflakeDestinationConfiguration,omitempty" cbor:"-"`
	IcebergDestinationConfiguration                 json.RawMessage `json:"IcebergDestinationConfiguration,omitempty" cbor:"-"`
	KinesisStreamSourceConfiguration                json.RawMessage `json:"KinesisStreamSourceConfiguration,omitempty" cbor:"-"`
	DeliveryStreamEncryptionConfigurationInput      json.RawMessage `json:"DeliveryStreamEncryptionConfigurationInput,omitempty" cbor:"-"`
}

// destinationConfigsByName returns req's destination-config members keyed by
// their own CreateDeliveryStream member name, for createDeliveryStreamTyped
// to walk in firehoseDestinationConfigNames' fixed order.
func (req *createDeliveryStreamReq) destinationConfigsByName() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"S3DestinationConfiguration":                      req.S3DestinationConfiguration,
		"ExtendedS3DestinationConfiguration":              req.ExtendedS3DestinationConfiguration,
		"RedshiftDestinationConfiguration":                req.RedshiftDestinationConfiguration,
		"ElasticsearchDestinationConfiguration":           req.ElasticsearchDestinationConfiguration,
		"AmazonopensearchserviceDestinationConfiguration": req.AmazonopensearchserviceDestinationConfiguration,
		"HttpEndpointDestinationConfiguration":            req.HttpEndpointDestinationConfiguration,
		"SplunkDestinationConfiguration":                  req.SplunkDestinationConfiguration,
		"SnowflakeDestinationConfiguration":               req.SnowflakeDestinationConfiguration,
		"IcebergDestinationConfiguration":                 req.IcebergDestinationConfiguration,
	}
}

type describeDeliveryStreamReq struct {
	DeliveryStreamName string `json:"DeliveryStreamName" cbor:"DeliveryStreamName"`
}

type deleteDeliveryStreamReq struct {
	DeliveryStreamName string `json:"DeliveryStreamName" cbor:"DeliveryStreamName"`
}

// firehoseRecord is the Record structure PutRecord and PutRecordBatch carry.
// Data is typed []byte so the decoder enforces the wire form itself — base64
// over JSON, a byte string over CBOR — instead of the raw message the batch
// handler used to count without ever decoding.
type firehoseRecord struct {
	Data []byte `json:"Data" cbor:"Data"`
}

type putRecordReq struct {
	DeliveryStreamName string          `json:"DeliveryStreamName" cbor:"DeliveryStreamName"`
	Record             *firehoseRecord `json:"Record" cbor:"Record"`
}

type putRecordBatchReq struct {
	DeliveryStreamName string           `json:"DeliveryStreamName" cbor:"DeliveryStreamName"`
	Records            []firehoseRecord `json:"Records" cbor:"Records"`
}

type createDeliveryStreamResp struct {
	DeliveryStreamARN string `json:"DeliveryStreamARN" cbor:"DeliveryStreamARN"`
}

type describeDeliveryStreamDescription struct {
	DeliveryStreamName   string `json:"DeliveryStreamName" cbor:"DeliveryStreamName"`
	DeliveryStreamARN    string `json:"DeliveryStreamARN" cbor:"DeliveryStreamARN"`
	DeliveryStreamStatus string `json:"DeliveryStreamStatus" cbor:"DeliveryStreamStatus"`
	DeliveryStreamType   string `json:"DeliveryStreamType" cbor:"DeliveryStreamType"`
	HasMoreDestinations  bool   `json:"HasMoreDestinations" cbor:"HasMoreDestinations"`
	Destinations         []any  `json:"Destinations" cbor:"Destinations"`
	// Source and DeliveryStreamEncryptionConfiguration are omitted (rather
	// than present-and-empty) whenever CreateDeliveryStream carried no
	// KinesisStreamSourceConfiguration / DeliveryStreamEncryptionConfigurationInput
	// — real DescribeDeliveryStream leaves both members out for a DirectPut,
	// unencrypted stream too.
	Source                                any `json:"Source,omitempty" cbor:"Source,omitempty"`
	DeliveryStreamEncryptionConfiguration any `json:"DeliveryStreamEncryptionConfiguration,omitempty" cbor:"DeliveryStreamEncryptionConfiguration,omitempty"`
}

type describeDeliveryStreamResp struct {
	DeliveryStreamDescription describeDeliveryStreamDescription `json:"DeliveryStreamDescription" cbor:"DeliveryStreamDescription"`
}

type listDeliveryStreamsResp struct {
	DeliveryStreamNames    []string `json:"DeliveryStreamNames" cbor:"DeliveryStreamNames"`
	HasMoreDeliveryStreams bool     `json:"HasMoreDeliveryStreams" cbor:"HasMoreDeliveryStreams"`
}

type putRecordResp struct {
	RecordId  string `json:"RecordId" cbor:"RecordId"`
	Encrypted bool   `json:"Encrypted" cbor:"Encrypted"`
}

type putRecordBatchResult struct {
	RecordId string `json:"RecordId" cbor:"RecordId"`
}

type putRecordBatchResp struct {
	FailedPutCount   int                    `json:"FailedPutCount" cbor:"FailedPutCount"`
	Encrypted        bool                   `json:"Encrypted" cbor:"Encrypted"`
	RequestResponses []putRecordBatchResult `json:"RequestResponses" cbor:"RequestResponses"`
}

func (s *Service) createDeliveryStreamTyped(ctx context.Context, req *createDeliveryStreamReq) (*createDeliveryStreamResp, *protocol.AWSError) {
	if aerr := validateDeliveryStreamName(req.DeliveryStreamName); aerr != nil {
		return nil, aerr
	}
	// A delivery stream name is unique per account per Region, so a second
	// create under the same name is ResourceInUseException — it used to
	// overwrite the existing stream's record silently (#149).
	if _, found := s.store.getStream(ctx, req.DeliveryStreamName); found {
		return nil, errStreamInUse(req.DeliveryStreamName)
	}
	tags := serviceutil.TagsFromList(req.Tags)
	// Validated before the stream is written (#1196) — a rejected create
	// leaves no delivery stream behind.
	if aerr := serviceutil.ValidateTags(firehoseTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	region := middleware.RegionFromContext(ctx, s.cfg.Region)
	arn := fmt.Sprintf("arn:aws:firehose:%s:%s:deliverystream/%s", region, s.cfg.AccountID, req.DeliveryStreamName)
	dsType := req.DeliveryStreamType
	if dsType == "" {
		if len(req.KinesisStreamSourceConfiguration) > 0 {
			// A KinesisStreamSourceConfiguration with no explicit
			// DeliveryStreamType names a KinesisStreamAsSource stream, not a
			// DirectPut one — the CloudFormation resource forwards this
			// property without also setting DeliveryStreamType whenever a
			// template follows AWS's own example template for it (#535).
			dsType = "KinesisStreamAsSource"
		} else {
			dsType = "DirectPut"
		}
	}

	var destinations []firehoseDestination
	configsByName := req.destinationConfigsByName()
	for _, name := range firehoseDestinationConfigNames {
		if cfgValue := configsByName[name]; len(cfgValue) > 0 {
			destinations = append(destinations, firehoseDestination{ConfigName: name, Config: cfgValue})
		}
	}

	ds := &DeliveryStream{
		DeliveryStreamName:      req.DeliveryStreamName,
		DeliveryStreamARN:       arn,
		DeliveryStreamStatus:    "ACTIVE",
		DeliveryStreamType:      dsType,
		Tags:                    tags,
		Destinations:            destinations,
		KinesisStreamSource:     req.KinesisStreamSourceConfiguration,
		EncryptionConfiguration: req.DeliveryStreamEncryptionConfigurationInput,
	}
	if err := s.store.putStream(ctx, ds); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &createDeliveryStreamResp{DeliveryStreamARN: arn}, nil
}

func (s *Service) describeDeliveryStreamTyped(ctx context.Context, req *describeDeliveryStreamReq) (*describeDeliveryStreamResp, *protocol.AWSError) {
	if aerr := validateDeliveryStreamName(req.DeliveryStreamName); aerr != nil {
		return nil, aerr
	}
	ds, found := s.store.getStream(ctx, req.DeliveryStreamName)
	if !found {
		return nil, errStreamNotFound(req.DeliveryStreamName)
	}
	return &describeDeliveryStreamResp{
		DeliveryStreamDescription: describeDeliveryStreamDescription{
			DeliveryStreamName:                    ds.DeliveryStreamName,
			DeliveryStreamARN:                     ds.DeliveryStreamARN,
			DeliveryStreamStatus:                  ds.DeliveryStreamStatus,
			DeliveryStreamType:                    ds.DeliveryStreamType,
			HasMoreDestinations:                   false,
			Destinations:                          firehoseDestinationDescriptions(ds.Destinations),
			Source:                                firehoseSourceDescription(ds.KinesisStreamSource),
			DeliveryStreamEncryptionConfiguration: firehoseEncryptionDescription(ds.EncryptionConfiguration),
		},
	}, nil
}

// firehoseDestinationDescriptions renders DescribeDeliveryStream's
// Destinations list from what CreateDeliveryStream stored: one entry per
// destination configuration given, each carrying a synthetic DestinationId
// and the same configuration object under its "...Description" member name.
// An empty, non-nil slice (never nil) matches what real Describe returns for
// a stream with no destinations configured.
func firehoseDestinationDescriptions(destinations []firehoseDestination) []any {
	out := make([]any, 0, len(destinations))
	for i, d := range destinations {
		descriptionName := strings.TrimSuffix(d.ConfigName, "Configuration") + "Description"
		out = append(out, map[string]any{
			"DestinationId": fmt.Sprintf("destinationId-%012d", i+1),
			descriptionName: d.Config,
		})
	}
	return out
}

// firehoseSourceDescription renders DescribeDeliveryStream's Source member
// from the KinesisStreamSourceConfiguration CreateDeliveryStream was given,
// or nil (omitted) when the stream has no stream source.
func firehoseSourceDescription(kinesisStreamSource json.RawMessage) any {
	if len(kinesisStreamSource) == 0 {
		return nil
	}
	return map[string]any{"KinesisStreamSourceDescription": kinesisStreamSource}
}

// firehoseEncryptionDescription renders DescribeDeliveryStream's
// DeliveryStreamEncryptionConfiguration from the
// DeliveryStreamEncryptionConfigurationInput CreateDeliveryStream was given
// (KeyARN/KeyType, echoed verbatim), adding Status — ENABLED, since Overcast
// applies no encryption and so has nothing that could fail to enable it — the
// one member the input shape does not carry. Returns nil (omitted) for an
// unencrypted stream, matching real Describe.
func firehoseEncryptionDescription(input json.RawMessage) any {
	if len(input) == 0 {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal(input, &fields); err != nil || fields == nil {
		fields = map[string]any{}
	}
	fields["Status"] = "ENABLED"
	return fields
}

func (s *Service) listDeliveryStreamsTyped(ctx context.Context, _ *struct{}) (*listDeliveryStreamsResp, *protocol.AWSError) {
	streams, err := s.store.listStreams(ctx)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	names := make([]string, 0, len(streams))
	for _, ds := range streams {
		names = append(names, ds.DeliveryStreamName)
	}
	return &listDeliveryStreamsResp{
		DeliveryStreamNames:    names,
		HasMoreDeliveryStreams: false,
	}, nil
}

func (s *Service) deleteDeliveryStreamTyped(ctx context.Context, req *deleteDeliveryStreamReq) (*struct{}, *protocol.AWSError) {
	if aerr := validateDeliveryStreamName(req.DeliveryStreamName); aerr != nil {
		return nil, aerr
	}
	if _, found := s.store.getStream(ctx, req.DeliveryStreamName); !found {
		return nil, errStreamNotFound(req.DeliveryStreamName)
	}
	if err := s.store.deleteStream(ctx, req.DeliveryStreamName); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &struct{}{}, nil
}

func (s *Service) putRecordTyped(ctx context.Context, req *putRecordReq) (*putRecordResp, *protocol.AWSError) {
	if aerr := validateDeliveryStreamName(req.DeliveryStreamName); aerr != nil {
		return nil, aerr
	}
	if aerr := validateRecord("record", req.Record); aerr != nil {
		return nil, aerr
	}
	if _, found := s.store.getStream(ctx, req.DeliveryStreamName); !found {
		return nil, errStreamNotFound(req.DeliveryStreamName)
	}
	// Accepted and discarded — nothing is delivered to a destination.
	return &putRecordResp{RecordId: uuid.NewString(), Encrypted: false}, nil
}

func (s *Service) putRecordBatchTyped(ctx context.Context, req *putRecordBatchReq) (*putRecordBatchResp, *protocol.AWSError) {
	if aerr := validateDeliveryStreamName(req.DeliveryStreamName); aerr != nil {
		return nil, aerr
	}
	if aerr := validateRecordBatch(req.Records); aerr != nil {
		return nil, aerr
	}
	if _, found := s.store.getStream(ctx, req.DeliveryStreamName); !found {
		return nil, errStreamNotFound(req.DeliveryStreamName)
	}
	// Accepted and discarded — one record id per entry, nothing delivered.
	results := make([]putRecordBatchResult, 0, len(req.Records))
	for range req.Records {
		results = append(results, putRecordBatchResult{RecordId: uuid.NewString()})
	}
	return &putRecordBatchResp{
		FailedPutCount:   0,
		Encrypted:        false,
		RequestResponses: results,
	}, nil
}

// ─── Tag operations ──────────────────────────────────────────────────────────

// Real Firehose sends Tags as a LIST of {Key,Value} structs, never a map.
type tagDeliveryStreamReq struct {
	DeliveryStreamName string        `json:"DeliveryStreamName" cbor:"DeliveryStreamName"`
	Tags               []firehoseTag `json:"Tags" cbor:"Tags"`
}

type untagDeliveryStreamReq struct {
	DeliveryStreamName string   `json:"DeliveryStreamName" cbor:"DeliveryStreamName"`
	TagKeys            []string `json:"TagKeys" cbor:"TagKeys"`
}

type listTagsForDeliveryStreamReq struct {
	DeliveryStreamName string `json:"DeliveryStreamName" cbor:"DeliveryStreamName"`
}

type listTagsForDeliveryStreamResp struct {
	Tags        []firehoseTag `json:"Tags" cbor:"Tags"`
	HasMoreTags bool          `json:"HasMoreTags" cbor:"HasMoreTags"`
}

type firehoseTag struct {
	Key   string `json:"Key" cbor:"Key"`
	Value string `json:"Value" cbor:"Value"`
}

func (s *Service) tagDeliveryStreamTyped(ctx context.Context, req *tagDeliveryStreamReq) (*struct{}, *protocol.AWSError) {
	incoming := make(map[string]string, len(req.Tags))
	for _, t := range req.Tags {
		incoming[t.Key] = t.Value
	}
	if aerr := serviceutil.ApplyInlineTags(ctx, req.DeliveryStreamName, incoming, firehoseTagCfg,
		func(ctx context.Context, name string) (*DeliveryStream, *protocol.AWSError) {
			ds, found := s.store.getStream(ctx, name)
			if !found {
				return nil, errStreamNotFound(name)
			}
			return ds, nil
		},
		func(ctx context.Context, ds *DeliveryStream) *protocol.AWSError {
			if err := s.store.putStream(ctx, ds); err != nil {
				return protocol.ErrInternalError
			}
			return nil
		},
	); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (s *Service) untagDeliveryStreamTyped(ctx context.Context, req *untagDeliveryStreamReq) (*struct{}, *protocol.AWSError) {
	if aerr := serviceutil.RemoveInlineTags(ctx, req.DeliveryStreamName, req.TagKeys,
		func(ctx context.Context, name string) (*DeliveryStream, *protocol.AWSError) {
			ds, found := s.store.getStream(ctx, name)
			if !found {
				return nil, errStreamNotFound(name)
			}
			return ds, nil
		},
		func(ctx context.Context, ds *DeliveryStream) *protocol.AWSError {
			if err := s.store.putStream(ctx, ds); err != nil {
				return protocol.ErrInternalError
			}
			return nil
		},
	); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (s *Service) listTagsForDeliveryStreamTyped(ctx context.Context, req *listTagsForDeliveryStreamReq) (*listTagsForDeliveryStreamResp, *protocol.AWSError) {
	tags, aerr := serviceutil.ListInlineTags(ctx, req.DeliveryStreamName,
		func(ctx context.Context, name string) (*DeliveryStream, *protocol.AWSError) {
			ds, found := s.store.getStream(ctx, name)
			if !found {
				return nil, errStreamNotFound(name)
			}
			return ds, nil
		},
	)
	if aerr != nil {
		return nil, aerr
	}
	tagList := serviceutil.TagElements(tags, func(k, v string) firehoseTag {
		return firehoseTag{Key: k, Value: v}
	})
	return &listTagsForDeliveryStreamResp{Tags: tagList, HasMoreTags: false}, nil
}
