package athena

import (
	"archive/tar"
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// engine_config.go — the configuration the engine container starts with,
// rendered into its /etc/athena and copied in before it starts.
//
// The tuning is the one measured for docs/plans/athena-s3tables-iceberg.md
// (Decision 1): a 512 MiB heap is the floor — 384m and 256m ran out of memory
// on ordinary queries — with G1, the C1 compiler only and a small code cache,
// and the memory and concurrency settings of a single-node engine that runs
// one small query at a time. Only the hive and iceberg plugins are loaded,
// through a plugin directory of links into the image's own.

const (
	// engineEtcDir is where the rendered configuration lands in the
	// container; the launcher is pointed at it.
	engineEtcDir = "/etc/athena"
	// enginePort is the port Trino's HTTP server listens on.
	enginePort = 8080
	// engineMinHeap is the smallest heap that runs ordinary queries.
	engineMinHeap = 512 << 20
	// engineImagePluginDir is where the Trino image installs its plugins.
	engineImagePluginDir = "/usr/lib/trino/plugin"

	// The two catalogs: Hive on the Glue metastore, which is what Athena
	// calls AwsDataCatalog, and Iceberg on the same Glue catalog, which the
	// Hive catalog redirects Iceberg tables to.
	hiveCatalog    = "awsdatacatalog"
	icebergCatalog = "awsdatacatalog_iceberg"

	// engineAccessKey signs the engine's calls to Overcast. Overcast does not
	// check credentials unless told to; see the Athena docs' limitations.
	engineAccessKey = "overcast-athena"
)

// enginePlugins are the only plugins the engine loads.
var enginePlugins = []string{"hive", "iceberg"}

// engineSettings are the inputs the configuration is rendered from.
type engineSettings struct {
	// Overcast is the origin the container reaches Overcast's API on, for
	// both Glue and S3.
	Overcast  string
	Region    string
	AccountID string
	// Memory is the container's memory limit in bytes.
	Memory int64
}

// heapBytes is the JVM heap: half the container, and never below the floor.
func (s engineSettings) heapBytes() int64 { return max(s.Memory/2, engineMinHeap) }

// renderEngineFiles returns each configuration file by its path under
// engineEtcDir.
func renderEngineFiles(s engineSettings) map[string]string {
	heapMB := s.heapBytes() >> 20
	return map[string]string{
		"jvm.config": lines(
			"-server",
			"-agentpath:/usr/lib/trino/bin/libjvmkill.so",
			fmt.Sprintf("-Xmx%dm", heapMB),
			fmt.Sprintf("-Xms%dm", heapMB/2),
			"-XX:+UseG1GC",
			"-XX:TieredStopAtLevel=1",
			"-XX:ReservedCodeCacheSize=64M",
			"-XX:+ExitOnOutOfMemoryError",
			"-XX:-OmitStackTraceInFastThrow",
			"-Djdk.attach.allowAttachSelf=true",
		),
		"config.properties": lines(
			"coordinator=true",
			"node-scheduler.include-coordinator=true",
			fmt.Sprintf("http-server.http.port=%d", enginePort),
			fmt.Sprintf("discovery.uri=http://localhost:%d", enginePort),
			"plugin.dir="+engineEtcDir+"/plugin",
			fmt.Sprintf("query.max-memory=%dMB", heapMB/2),
			fmt.Sprintf("query.max-memory-per-node=%dMB", heapMB/4),
			fmt.Sprintf("memory.heap-headroom-per-node=%dMB", heapMB/8),
			"task.concurrency=1",
			"task.max-worker-threads=4",
			"exchange.max-buffer-size=8MB",
			"sink.max-buffer-size=8MB",
			"task.max-partial-aggregation-memory=4MB",
			"query.max-history=10",
		),
		"node.properties": lines(
			"node.environment=overcast",
			"node.id=overcast-athena",
			"node.data-dir=/data/trino",
		),
		"log.properties": lines("io.trino=WARN"),
		"catalog/" + hiveCatalog + ".properties": lines(append([]string{
			"connector.name=hive",
			"hive.metastore=glue",
			"hive.iceberg-catalog-name=" + icebergCatalog,
			"hive.non-managed-table-writes-enabled=true",
			"hive.non-managed-table-creates-enabled=true",
		}, s.awsProperties()...)...),
		"catalog/" + icebergCatalog + ".properties": lines(append([]string{
			"connector.name=iceberg",
			"iceberg.catalog.type=glue",
		}, s.awsProperties()...)...),
	}
}

// awsProperties point a catalog's Glue metastore and its S3 file system at
// Overcast. S3 is path-style: the endpoint is an address, not a name a
// bucket can be prefixed to.
func (s engineSettings) awsProperties() []string {
	return []string{
		"hive.metastore.glue.region=" + s.Region,
		"hive.metastore.glue.endpoint-url=" + s.Overcast,
		"hive.metastore.glue.catalogid=" + s.AccountID,
		"hive.metastore.glue.aws-access-key=" + engineAccessKey,
		"hive.metastore.glue.aws-secret-key=" + engineAccessKey,
		"fs.native-s3.enabled=true",
		"s3.endpoint=" + s.Overcast,
		"s3.region=" + s.Region,
		"s3.path-style-access=true",
		"s3.aws-access-key=" + engineAccessKey,
		"s3.aws-secret-key=" + engineAccessKey,
	}
}

func lines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

// engineConfigArchive is the tar CopyToContainer extracts at the container's
// root: the rendered files under engineEtcDir, and the plugin directory as
// links to the image's own plugins.
func engineConfigArchive(files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		body := files[name]
		hdr := &tar.Header{Name: strings.TrimPrefix(engineEtcDir, "/") + "/" + name, Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			return nil, err
		}
	}
	for _, plugin := range enginePlugins {
		hdr := &tar.Header{Typeflag: tar.TypeSymlink, Name: strings.TrimPrefix(engineEtcDir, "/") + "/plugin/" + plugin,
			Linkname: engineImagePluginDir + "/" + plugin, Mode: 0o777}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
