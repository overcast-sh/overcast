package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast/internal/awsmodel"
)

// The pruned shape snapshot.
//
// The routing manifest deliberately carries no shapes. Inert-tier codegen
// (docs/plans/inert-tier-rollout.md §4.6) and the compat scenario generator
// (docs/plans/compat-coverage-modelgen.md §3.7) both need them, and neither may
// vendor the raw Smithy corpus (docs/plans/aws-api-operation-coverage.md §3).
//
// The reconciliation is this pruner: for a reviewed list of in-scope services it
// writes models/aws/shapes/<service>.json holding only the shapes transitively
// reachable from that service's operations and resources, with traits filtered
// to the allowlist the generators actually consume. It is a generated,
// byte-deterministic derivative — generator input only, never read at runtime —
// with its own shapes-sha256 in models/aws/VERSION so an ordinary pull request
// can validate it with no network and no model checkout.
//
// One pruner, one digest, two consumers: do not build a second distillation.
//
// The emitted document's vocabulary — awsmodel.Snapshot, awsmodel.SnapshotShape
// and awsmodel.SnapshotMember — is declared in internal/awsmodel/snapshot.go,
// where the consumer side can import the same types instead of restating them.

// shapeTraitAllowlist names every Smithy trait the snapshot keeps. Everything
// else — documentation, examples, waiters, smoke tests, endpoint rule sets — is
// dropped. Adding an entry is a deliberate, reviewed act: it widens the
// committed snapshot for all in-scope services at once.
var shapeTraitAllowlist = map[string]struct{}{
	// Structure and member semantics (inert-tier-rollout.md §3.2, §3.4).
	"smithy.api#required":         {},
	"smithy.api#default":          {},
	"smithy.api#clientOptional":   {}, // a @required member the service does not actually require
	"smithy.api#enum":             {}, // Smithy 1.0 string enums
	"smithy.api#enumValue":        {}, // Smithy 2.0 enum member wire values
	"smithy.api#length":           {},
	"smithy.api#range":            {},
	"smithy.api#pattern":          {},
	"smithy.api#sparse":           {},
	"smithy.api#idempotencyToken": {},
	"smithy.api#timestampFormat":  {},
	"smithy.api#mediaType":        {},
	"smithy.api#references":       {},
	"smithy.api#input":            {}, // marks a structure as one operation's dedicated input
	"smithy.api#output":           {}, // ... and output; decides In/Out types vs shared records

	// Errors (inert-tier-rollout.md §3.3).
	"smithy.api#error":            {},
	"smithy.api#httpError":        {},
	"aws.protocols#awsQueryError": {}, // Query wire error code and status, not the shape name

	// HTTP bindings.
	"smithy.api#http":                 {},
	"smithy.api#httpLabel":            {},
	"smithy.api#httpQuery":            {},
	"smithy.api#httpQueryParams":      {},
	"smithy.api#httpHeader":           {},
	"smithy.api#httpPrefixHeaders":    {},
	"smithy.api#httpPayload":          {},
	"smithy.api#httpResponseCode":     {},
	"smithy.api#httpChecksumRequired": {},

	// Serialisation names (inert-tier-rollout.md §4.2: the codecs serialise via
	// struct tags, and the tags come from these).
	"smithy.api#jsonName":        {},
	"smithy.api#xmlName":         {},
	"smithy.api#xmlFlattened":    {},
	"smithy.api#xmlAttribute":    {},
	"smithy.api#xmlNamespace":    {},
	"aws.protocols#ec2QueryName": {},

	// Pagination (inert-tier-rollout.md §3.1 List, §3.3 invalid-token).
	"smithy.api#paginated": {},

	// Service identity, signing name and protocol family. The ARN template of
	// §3.5 is built from the sigv4 signing name; the codec family decides the
	// wire encoding for every generated type.
	"aws.api#service":            {},
	"aws.auth#sigv4":             {},
	"aws.protocols#awsJson1_0":   {},
	"aws.protocols#awsJson1_1":   {},
	"aws.protocols#awsQuery":     {},
	"aws.protocols#ec2Query":     {},
	"aws.protocols#restJson1":    {},
	"aws.protocols#restXml":      {},
	"smithy.protocols#rpcv2Cbor": {},
	"smithy.protocols#rpcv2Json": {},
	// A JSON-protocol service migrated from Query, which still answers with the
	// Query error code in the x-amzn-query-error header. An interpreter has to
	// know which code an SDK may surface (compat/model/README.md § Assertions).
	"aws.protocols#awsQueryCompatible": {},

	// Resource metadata the lifecycle walk depends on (§3.1 Tag, §3.5 ARNs).
	"aws.api#arn":      {},
	"aws.api#taggable": {},
}

// rawShape is the full Smithy JSON AST shape. main.go's narrower shape type
// carries only what the routing manifest needs; the pruner needs members,
// identifiers and the rest of the resource vocabulary as well.
type rawShape struct {
	Type                 string                        `json:"type"`
	Version              string                        `json:"version"`
	Input                *awsmodel.Reference           `json:"input"`
	Output               *awsmodel.Reference           `json:"output"`
	Errors               []awsmodel.Reference          `json:"errors"`
	Member               *rawMember                    `json:"member"`
	Key                  *rawMember                    `json:"key"`
	Value                *rawMember                    `json:"value"`
	Members              map[string]rawMember          `json:"members"`
	Identifiers          map[string]awsmodel.Reference `json:"identifiers"`
	Properties           map[string]awsmodel.Reference `json:"properties"`
	Create               *awsmodel.Reference           `json:"create"`
	Put                  *awsmodel.Reference           `json:"put"`
	Read                 *awsmodel.Reference           `json:"read"`
	Update               *awsmodel.Reference           `json:"update"`
	Delete               *awsmodel.Reference           `json:"delete"`
	List                 *awsmodel.Reference           `json:"list"`
	Operations           []awsmodel.Reference          `json:"operations"`
	CollectionOperations []awsmodel.Reference          `json:"collectionOperations"`
	Resources            []awsmodel.Reference          `json:"resources"`
	Traits               map[string]json.RawMessage    `json:"traits"`
}

type rawMember struct {
	Target string                     `json:"target"`
	Traits map[string]json.RawMessage `json:"traits"`
}

// reference is the member's target as a plain reference, nil for an absent
// member.
func (m *rawMember) reference() *awsmodel.Reference {
	if m == nil {
		return nil
	}
	return &awsmodel.Reference{Target: m.Target}
}

type rawModel struct {
	Smithy string              `json:"smithy"`
	Shapes map[string]rawShape `json:"shapes"`
}

// readShapeServices parses the reviewed in-scope service list: one canonical
// service key per line, '#' comments and blank lines ignored. Reviewed data,
// not a heuristic — a service enters the snapshot because someone put it here.
func readShapeServices(path string) ([]string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read shape service list %s: %w", path, err)
	}
	var services []string
	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(contents), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("%s: service %q listed twice", path, name)
		}
		seen[name] = struct{}{}
		services = append(services, name)
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("%s lists no services", path)
	}
	sort.Strings(services)
	return services, nil
}

// buildShapeSnapshots prunes each in-scope service's model. It fails loudly on a
// listed service the corpus does not contain: a silently missing snapshot would
// be read by the inert generator as "this service has no operations".
func buildShapeSnapshots(modelsDir string, services []string) (map[string][]byte, error) {
	snapshots, err := pruneServices(modelsDir, services, shapeTraitAllowlist)
	if err != nil {
		return nil, err
	}
	rendered := make(map[string][]byte, len(snapshots))
	for service, snapshot := range snapshots {
		contents, err := renderShapeSnapshot(snapshot)
		if err != nil {
			return nil, err
		}
		rendered[service] = contents
	}
	return rendered, nil
}

// pruneServices walks the corpus once and prunes every listed service with the
// given trait allowlist. It is the one pruner both committed shape artifacts are
// cut from: the snapshot here, and the runtime SDK shape tables in
// sdkshapes.go, which differ only in the traits they keep and how they render.
func pruneServices(modelsDir string, services []string, allow map[string]struct{}) (map[string]awsmodel.Snapshot, error) {
	wanted := make(map[string]struct{}, len(services))
	for _, service := range services {
		wanted[service] = struct{}{}
	}
	pruned := make(map[string]awsmodel.Snapshot, len(services))
	err := filepath.WalkDir(modelsDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" || len(wanted) == 0 {
			return nil
		}
		snapshots, err := pruneModel(path, wanted, allow)
		if err != nil {
			return err
		}
		for _, snapshot := range snapshots {
			pruned[snapshot.Service] = snapshot
			delete(wanted, snapshot.Service)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("prune shapes: %w", err)
	}
	if len(wanted) > 0 {
		missing := make([]string, 0, len(wanted))
		for service := range wanted {
			missing = append(missing, service)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("prune shapes: no model defines service(s) %s", strings.Join(missing, ", "))
	}
	return pruned, nil
}

// pruneModel returns a snapshot for each in-scope service the file defines.
func pruneModel(path string, wanted map[string]struct{}, allow map[string]struct{}) ([]awsmodel.Snapshot, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var parsed rawModel
	if err := json.Unmarshal(contents, &parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	serviceIDs := make([]string, 0, 1)
	for shapeID, svc := range parsed.Shapes {
		if svc.Type == "service" {
			serviceIDs = append(serviceIDs, shapeID)
		}
	}
	sort.Strings(serviceIDs)
	var snapshots []awsmodel.Snapshot
	for _, shapeID := range serviceIDs {
		svc := parsed.Shapes[shapeID]
		rawTrait, ok := svc.Traits["aws.api#service"]
		if !ok {
			return nil, fmt.Errorf("%s: service %s has no aws.api#service trait", path, shapeID)
		}
		var trait awsmodel.ServiceTrait
		if err := json.Unmarshal(rawTrait, &trait); err != nil {
			return nil, fmt.Errorf("parse service trait in %s: %w", path, err)
		}
		service := strings.ToLower(strings.ReplaceAll(trait.SDKID, " ", "-"))
		if _, in := wanted[service]; !in {
			continue
		}
		snapshot, err := pruneService(parsed, shapeID, service, trait.SDKID, allow)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

// pruneService walks everything reachable from the service shape.
func pruneService(parsed rawModel, serviceID, service, sdkID string, allow map[string]struct{}) (awsmodel.Snapshot, error) {
	namespace := serviceID[:strings.LastIndex(serviceID, "#")]
	snapshot := awsmodel.Snapshot{
		Service:      service,
		Smithy:       parsed.Smithy,
		Namespace:    namespace,
		ServiceShape: awsmodel.ShapeName(serviceID),
		SDKID:        sdkID,
		APIVersion:   parsed.Shapes[serviceID].Version,
		Protocols:    awsmodel.ModelProtocols(parsed.Shapes[serviceID].Traits),
		Shapes:       make(map[string]awsmodel.SnapshotShape),
	}
	relative := func(id string) string {
		if after, ok := strings.CutPrefix(id, namespace+"#"); ok {
			return after
		}
		return id
	}

	visited := make(map[string]struct{})
	var visit func(id string) error
	visitRef := func(ref *awsmodel.Reference) (string, error) {
		if ref == nil {
			return "", nil
		}
		if err := visit(ref.Target); err != nil {
			return "", err
		}
		return relative(ref.Target), nil
	}
	visitRefs := func(refs []awsmodel.Reference) ([]string, error) {
		if len(refs) == 0 {
			return nil, nil
		}
		out := make([]string, 0, len(refs))
		for _, ref := range refs {
			if err := visit(ref.Target); err != nil {
				return nil, err
			}
			out = append(out, relative(ref.Target))
		}
		// Sorted so a reordering upstream cannot churn the committed file; the
		// AST attaches no meaning to the order of these reference lists.
		sort.Strings(out)
		return slices.Compact(out), nil
	}
	visitMap := func(refs map[string]awsmodel.Reference) (map[string]string, error) {
		if len(refs) == 0 {
			return nil, nil
		}
		out := make(map[string]string, len(refs))
		for name, ref := range refs {
			if err := visit(ref.Target); err != nil {
				return nil, err
			}
			out[name] = relative(ref.Target)
		}
		return out, nil
	}

	visit = func(id string) error {
		if _, seen := visited[id]; seen {
			return nil
		}
		// Prelude shapes (smithy.api#String, smithy.api#Unit, …) are implicit:
		// they are targets, never definitions, so there is nothing to emit.
		if strings.HasPrefix(id, "smithy.api#") {
			return nil
		}
		raw, ok := parsed.Shapes[id]
		if !ok {
			return fmt.Errorf("shape %s references missing shape %s", serviceID, id)
		}
		visited[id] = struct{}{}

		out := awsmodel.SnapshotShape{Type: raw.Type, Version: raw.Version}
		var err error
		for _, link := range []struct {
			ref  *awsmodel.Reference
			dest *string
		}{
			{raw.Input, &out.Input}, {raw.Output, &out.Output},
			{raw.Member.reference(), &out.Member}, {raw.Key.reference(), &out.Key}, {raw.Value.reference(), &out.Value},
			{raw.Create, &out.Create}, {raw.Put, &out.Put}, {raw.Read, &out.Read},
			{raw.Update, &out.Update}, {raw.Delete, &out.Delete}, {raw.List, &out.List},
		} {
			if *link.dest, err = visitRef(link.ref); err != nil {
				return err
			}
		}
		for _, list := range []struct {
			refs []awsmodel.Reference
			dest *[]string
		}{
			{raw.Errors, &out.Errors}, {raw.Operations, &out.Operations},
			{raw.CollectionOperations, &out.CollectionOperations}, {raw.Resources, &out.Resources},
		} {
			if *list.dest, err = visitRefs(list.refs); err != nil {
				return err
			}
		}
		// A list's or map's member can carry traits of its own (@xmlName on a
		// list member names each element). The snapshot has no field for them,
		// so they are held on the in-memory shape only — see
		// awsmodel.SnapshotShape.MemberTraits.
		for _, link := range []struct {
			member *rawMember
			dest   *map[string]json.RawMessage
		}{
			{raw.Member, &out.MemberTraits}, {raw.Key, &out.KeyTraits}, {raw.Value, &out.ValueTraits},
		} {
			if link.member == nil {
				continue
			}
			if *link.dest, err = filterTraits(link.member.Traits, allow); err != nil {
				return fmt.Errorf("shape %s: %w", id, err)
			}
		}
		if out.Identifiers, err = visitMap(raw.Identifiers); err != nil {
			return err
		}
		if out.Properties, err = visitMap(raw.Properties); err != nil {
			return err
		}
		if len(raw.Members) > 0 {
			out.Members = make(map[string]awsmodel.SnapshotMember, len(raw.Members))
			for name, member := range raw.Members {
				if err := visit(member.Target); err != nil {
					return err
				}
				traits, err := filterTraits(member.Traits, allow)
				if err != nil {
					return fmt.Errorf("member %s of %s: %w", name, id, err)
				}
				out.Members[name] = awsmodel.SnapshotMember{Target: relative(member.Target), Traits: traits}
			}
		}
		if out.Traits, err = filterTraits(raw.Traits, allow); err != nil {
			return fmt.Errorf("shape %s: %w", id, err)
		}
		snapshot.Shapes[relative(id)] = out
		return nil
	}
	if err := visit(serviceID); err != nil {
		return awsmodel.Snapshot{}, err
	}
	return snapshot, nil
}

// filterTraits keeps the allowlisted traits and normalises their values.
//
// Values are decoded and re-encoded rather than copied, so object keys inside a
// trait come out sorted and whitespace comes out uniform regardless of how the
// upstream file was formatted. json.Number preserves each numeric literal
// exactly, so no precision or notation is invented.
func filterTraits(traits map[string]json.RawMessage, allow map[string]struct{}) (map[string]json.RawMessage, error) {
	if len(traits) == 0 {
		return nil, nil
	}
	var kept map[string]json.RawMessage
	for name, value := range traits {
		if _, allowed := allow[name]; !allowed {
			continue
		}
		normalized, err := normalizeJSON(value)
		if err != nil {
			return nil, fmt.Errorf("trait %s: %w", name, err)
		}
		if kept == nil {
			kept = make(map[string]json.RawMessage, len(traits))
		}
		kept[name] = normalized
	}
	return kept, nil
}

func normalizeJSON(value json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return encodeJSON(decoded)
}

// encodeJSON marshals without HTML escaping and without the trailing newline
// json.Encoder adds. Escaping '<' and '&' would obscure @pattern values for no
// benefit: nothing here is ever embedded in a web page.
func encodeJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

// renderShapeSnapshot emits one service document: a fixed-order header, then one
// line per shape in sorted shape-name order.
//
// One line per shape rather than fully compact or fully indented. Compact would
// make a model refresh an unreviewable single-line diff; indenting the whole
// document costs about 40% more bytes for a file no human edits. Line-per-shape
// costs one byte per shape and makes the diff read as "these shapes changed".
func renderShapeSnapshot(snapshot awsmodel.Snapshot) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString("{\n")
	for _, field := range []struct {
		name  string
		value any
	}{
		{"smithy", snapshot.Smithy},
		{"service", snapshot.Service},
		{"namespace", snapshot.Namespace},
		{"serviceShape", snapshot.ServiceShape},
		{"sdkId", snapshot.SDKID},
		{"apiVersion", snapshot.APIVersion},
		{"protocols", snapshot.Protocols},
	} {
		encoded, err := encodeJSON(field.value)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", field.name, err)
		}
		fmt.Fprintf(&out, "%q: %s,\n", field.name, encoded)
	}
	out.WriteString("\"shapes\": {\n")
	names := make([]string, 0, len(snapshot.Shapes))
	for name := range snapshot.Shapes {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		encoded, err := encodeJSON(snapshot.Shapes[name])
		if err != nil {
			return nil, fmt.Errorf("encode shape %s: %w", name, err)
		}
		separator := ",\n"
		if i == len(names)-1 {
			separator = "\n"
		}
		fmt.Fprintf(&out, "%q: %s%s", name, encoded, separator)
	}
	out.WriteString("}\n}\n")
	return out.Bytes(), nil
}

// ShapesDigestField names the provenance entry recording the SHA-256 of the
// whole pruned snapshot directory.
const ShapesDigestField = "shapes-sha256"

// ShapesDigest hashes the snapshot directory as a set of files, so a hand-edit,
// a deletion, or a partial merge all change it.
//
// The definition, reproducible by hand: for each file, the line
//
//	<sha256 of the file contents in lowercase hex><two spaces><path relative to the snapshot directory, with '/' separators><newline>
//
// sort those lines bytewise, concatenate, and take the SHA-256 of the result in
// lowercase hex. That is the format `sha256sum` prints in text mode, so
// `cd models/aws/shapes && sha256sum -t *.json | LC_ALL=C sort | sha256sum -t`
// reproduces it by hand (-t is already the default outside Windows).
func ShapesDigest(files map[string][]byte) string {
	lines := make([]string, 0, len(files))
	for name, contents := range files {
		sum := sha256.Sum256(contents)
		lines = append(lines, fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name))
	}
	sort.Strings(lines)
	digest := sha256.Sum256([]byte(strings.Join(lines, "")))
	return hex.EncodeToString(digest[:])
}

// snapshotFileName maps a service key onto its file inside the snapshot
// directory. Service keys come from SDK IDs, which are alphanumerics, spaces
// and hyphens, so this cannot escape the directory; it is asserted rather than
// assumed because the key ends up in a path.
func snapshotFileName(service string) (string, error) {
	if service == "" || strings.ContainsAny(service, `/\.`) || service != strings.TrimSpace(service) {
		return "", fmt.Errorf("service key %q is not a usable file name", service)
	}
	return service + ".json", nil
}

// writeOrCheckShapes writes the snapshot, or in check mode proves the committed
// one is byte-for-byte what the generator just produced — including that it
// holds no extra files, which is how a service dropped from the reviewed list
// would otherwise linger. It takes the file-name-keyed form that ShapesDigest
// hashes, so the bytes written and the bytes digested cannot diverge.
func writeOrCheckShapes(dir string, files map[string][]byte, check bool) error {
	if check {
		return checkShapes(dir, files)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create shape snapshot directory %s: %w", dir, err)
	}
	existing, err := listShapeFiles(dir)
	if err != nil {
		return err
	}
	for name := range existing {
		if _, wanted := files[name]; !wanted {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return fmt.Errorf("remove stale shape snapshot %s: %w", name, err)
			}
		}
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o644); err != nil {
			return fmt.Errorf("write shape snapshot %s: %w", name, err)
		}
	}
	return nil
}

func checkShapes(dir string, files map[string][]byte) error {
	existing, err := listShapeFiles(dir)
	if err != nil {
		return err
	}
	for name := range existing {
		if _, wanted := files[name]; !wanted {
			return fmt.Errorf("%s holds %s, which no reviewed in-scope service produces; regenerate with make generate-aws-operations", dir, name)
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		current, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("read shape snapshot %s: %w", name, err)
		}
		if !bytes.Equal(current, files[name]) {
			return fmt.Errorf("%s is stale; run make generate-aws-operations with the pinned AWS model checkout", filepath.Join(dir, name))
		}
	}
	return nil
}

func listShapeFiles(dir string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, fmt.Errorf("read shape snapshot directory %s: %w", dir, err)
	}
	files := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		files[entry.Name()] = struct{}{}
	}
	return files, nil
}

// shapeSnapshotFiles re-keys the rendered snapshot by file name, which is the
// form both ShapesDigest and writeOrCheckShapes work in.
func shapeSnapshotFiles(rendered map[string][]byte) (map[string][]byte, error) {
	files := make(map[string][]byte, len(rendered))
	for service, contents := range rendered {
		name, err := snapshotFileName(service)
		if err != nil {
			return nil, err
		}
		files[name] = contents
	}
	return files, nil
}
