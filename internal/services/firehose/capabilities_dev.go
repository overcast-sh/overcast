//go:build dev

package firehose

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		// Delivery Streams
		capabilities.Capability{Service: "firehose", Operation: "CreateDeliveryStream", Category: "Delivery Streams", Status: capabilities.StatusSupported, Notes: "Creates a delivery stream; destination, Kinesis stream source and encryption configurations are stored and echoed by DescribeDeliveryStream, but nothing is ever delivered to a destination"},
		capabilities.Capability{Service: "firehose", Operation: "DescribeDeliveryStream", Category: "Delivery Streams", Status: capabilities.StatusSupported, Notes: "Returns delivery stream details, including the destination(s), stream source and encryption configuration given at creation"},
		capabilities.Capability{Service: "firehose", Operation: "ListDeliveryStreams", Category: "Delivery Streams", Status: capabilities.StatusSupported, Notes: "Lists all delivery streams"},
		capabilities.Capability{Service: "firehose", Operation: "DeleteDeliveryStream", Category: "Delivery Streams", Status: capabilities.StatusSupported, Notes:
		// Records
		"Deletes a delivery stream"},

		capabilities.Capability{Service: "firehose", Operation: "PutRecord", Category: "Records", Status: capabilities.StatusSupported, Notes: "Writes a single record to the stream"},
		capabilities.Capability{Service: "firehose", Operation: "PutRecordBatch", Category: "Records", Status: capabilities.StatusSupported, Notes: "Writes multiple records to the stream"},

		// Tags
		capabilities.Capability{Service: "firehose", Operation: "TagDeliveryStream", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "Adds or overwrites tags on a delivery stream"},
		capabilities.Capability{Service: "firehose", Operation: "UntagDeliveryStream", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "Removes tags by key from a delivery stream"},
		capabilities.Capability{Service: "firehose", Operation: "ListTagsForDeliveryStream", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "Returns tags for a delivery stream"},
	)
}
