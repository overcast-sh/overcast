package debugger

import (
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/hostpath"
)

// Service is a compute service the debugger attaches to. It picks the
// per-service flag a warning names, the tagging command the console shows,
// and the first segment of a target id.
type Service string

const (
	// ServiceLambda is the Lambda function service.
	ServiceLambda Service = "lambda"
	// ServiceECS is the ECS task service.
	ServiceECS Service = "ecs"
)

// FlagEnv is the environment variable that enables the debugger for this
// service, named in every "requested but off" warning so the fix is one
// copy-paste away.
func (s Service) FlagEnv() string {
	return "OVERCAST_" + strings.ToUpper(string(s)) + "_DEBUGGER"
}

// Tag keys. They are overcast: tags because a tag is inert metadata AWS
// stores and ignores, so nothing here can change what a deploy does.
const (
	// TagDebug enables debugging with an auto-allocated port.
	TagDebug = "overcast:debug"
	// TagPort enables debugging on a fixed port; it implies TagDebug.
	TagPort = "overcast:debug-port"
	// TagProtocol overrides protocol resolution: inspector, jdwp, dap or
	// passthrough. Required for images and custom runtimes when nothing in
	// the environment gives the protocol away.
	TagProtocol = "overcast:debug-protocol"
	// TagSourcePath is the local root editors map the container's code root
	// to. It falls back to TagHotReloadPath, which usually names the same tree.
	TagSourcePath = "overcast:source-path"
	// TagHotReloadPath is hot reload's tag, read here only as the fallback
	// for TagSourcePath.
	TagHotReloadPath = "overcast:hot-reload-path"
)

// Spec is what the tags asked for, for one resource (or one ECS container),
// after validation. It carries the service flag too, so a target can be
// registered as inert — present in the console, explaining why it is off —
// when a tag asks for a debugger the server has not enabled.
type Spec struct {
	// Service the resource belongs to.
	Service Service
	// Container is the ECS container the spec applies to; empty for Lambda.
	Container string
	// Tagged reports that a tag asked for debugging.
	Tagged bool
	// FlagOn is the service's OVERCAST_<SERVICE>_DEBUGGER flag.
	FlagOn bool
	// Port is the fixed port from TagPort; 0 means auto-allocate.
	Port int
	// Protocol is the TagProtocol override, validated against the default
	// registry; empty means resolve.
	Protocol string
	// SourcePath is the local root as the daemon would see it (normalised
	// through hostpath), used to decide whether a root is known.
	SourcePath string
	// SourcePathRaw is the local root exactly as the user wrote it — the
	// form their editor wants, which on Windows is not the normalised one.
	SourcePathRaw string
}

// Enabled reports whether a debugger will actually be offered: a tag asked
// and the server allows it.
func (s Spec) Enabled() bool { return s.Tagged && s.FlagOn }

// Problem is one tag the parser could not honour, in a shape the caller logs
// at WARN. The caller owns the logger and the log line — this package only
// says what was wrong and what fixes it, mirroring how hot reload reports
// its tags.
type Problem struct {
	// Key is the tag key as written, including any container suffix.
	Key string
	// Value is the offending value, empty when the key itself is the problem.
	Value string
	// Reason is one sentence saying what could not be honoured.
	Reason string
	// Hint names the fix.
	Hint string
}

// Fields renders the problem as structured log fields, so every service logs
// it the same way.
func (p Problem) Fields() []zap.Field {
	fields := make([]zap.Field, 0, 4)
	fields = append(fields, zap.String("tag", p.Key))
	if p.Value != "" {
		fields = append(fields, zap.String("value", p.Value))
	}
	fields = append(fields, zap.String("reason", p.Reason))
	if p.Hint != "" {
		fields = append(fields, zap.String("hint", p.Hint))
	}
	return fields
}

// flagOffProblem is the one warning a tagged-but-disabled resource earns,
// naming the flag exactly as hot reload does.
func flagOffProblem(service Service) Problem {
	return Problem{
		Key:    TagDebug,
		Reason: "debugger requested by tag but not enabled — the resource runs as if untagged",
		Hint:   "start Overcast with " + service.FlagEnv() + "=true (or OVERCAST_DEBUGGER=true for every compute service)",
	}
}

// tagValues is one resource's (or one container's) raw tag values by key.
type tagValues map[string]string

// isDebuggerTag reports whether key is one this package reads, with or without
// a container suffix. It is the fast path: a resource with no such tag costs
// one prefix check per tag and no allocation.
func isDebuggerTag(key string) bool {
	for _, base := range [...]string{TagDebug, TagPort, TagProtocol, TagSourcePath, TagHotReloadPath} {
		if key == base || strings.HasPrefix(key, base+"/") {
			return true
		}
	}
	return false
}

// SpecFromTags reads a Lambda function's tags. Every value that cannot be
// honoured is reported and dropped, so the function runs as it would without
// that tag: AWS accepts any tag, so nothing here may fail a deploy.
func SpecFromTags(service Service, tags map[string]string, flagOn bool) (Spec, []Problem) {
	var values tagValues
	var problems []Problem
	for key, value := range tags {
		if !isDebuggerTag(key) {
			continue
		}
		if strings.Contains(key, "/") {
			problems = append(problems, Problem{Key: key, Value: value,
				Reason: "container suffixes apply to ECS task definitions only",
				Hint:   "use the bare key " + key[:strings.IndexByte(key, '/')]})
			continue
		}
		if values == nil {
			values = tagValues{}
		}
		values[key] = value
	}
	spec, problems := specFromValues(service, "", values, problems)
	if spec.Tagged && !flagOn {
		problems = append(problems, flagOffProblem(service))
	}
	spec.FlagOn = flagOn
	return spec, problems
}

// SpecsFromTaskTags reads an ECS task definition's tags for the named
// containers. A key suffixed "/<container>" names its container and wins; the
// bare key is a convenience for the single-container task and is refused,
// naming the candidates, when it would have to guess. The result holds only
// containers a tag addressed, so the common untagged task allocates nothing.
func SpecsFromTaskTags(tags map[string]string, containers []string, flagOn bool) (map[string]Spec, []Problem) {
	var bare tagValues
	var suffixed map[string]tagValues
	var problems []Problem
	for key, value := range tags {
		if !isDebuggerTag(key) {
			continue
		}
		base, container, hasSuffix := strings.Cut(key, "/")
		switch {
		case !hasSuffix:
			if bare == nil {
				bare = tagValues{}
			}
			bare[key] = value
		case container == "":
			problems = append(problems, Problem{Key: key, Value: value,
				Reason: "the container suffix is empty",
				Hint:   "name the container, for example " + base + "/" + firstOr(containers, "app")})
		case !slices.Contains(containers, container):
			problems = append(problems, Problem{Key: key, Value: value,
				Reason: "the task definition declares no such container",
				Hint:   "declared containers: " + strings.Join(containers, ", ")})
		default:
			if suffixed == nil {
				suffixed = map[string]tagValues{}
			}
			if suffixed[container] == nil {
				suffixed[container] = tagValues{}
			}
			suffixed[container][base] = value
		}
	}
	if bare == nil && suffixed == nil {
		return nil, problems
	}

	// The bare key only ever means "the one container". With several, the
	// bare values are dropped rather than applied to all of them: two
	// containers cannot share a fixed port, and a debugger on every sidecar
	// is never what was meant.
	if bare != nil {
		switch len(containers) {
		case 1:
			name := containers[0]
			if suffixed == nil {
				suffixed = map[string]tagValues{}
			}
			merged := tagValues{}
			for k, v := range bare {
				merged[k] = v
			}
			for k, v := range suffixed[name] {
				merged[k] = v
			}
			suffixed[name] = merged
		case 0:
			problems = append(problems, Problem{Key: firstKey(bare),
				Reason: "the task definition declares no container"})
		default:
			problems = append(problems, Problem{Key: firstKey(bare),
				Reason: "the task definition declares more than one container, so the bare tag is ambiguous",
				Hint:   "name the container in the tag key, for example " + firstKey(bare) + "/" + containers[0]})
		}
	}

	specs := make(map[string]Spec, len(suffixed))
	tagged := false
	for _, name := range containers {
		values, ok := suffixed[name]
		if !ok {
			continue
		}
		var spec Spec
		spec, problems = specFromValues(ServiceECS, name, values, problems)
		spec.FlagOn = flagOn
		tagged = tagged || spec.Tagged
		specs[name] = spec
	}
	if tagged && !flagOn {
		problems = append(problems, flagOffProblem(ServiceECS))
	}
	return specs, problems
}

// specFromValues validates one resource's tag values. container is only used
// to name the key in problems, so an ECS warning says which container it is
// about.
func specFromValues(service Service, container string, values tagValues, problems []Problem) (Spec, []Problem) {
	spec := Spec{Service: service, Container: container}
	keyFor := func(base string) string {
		if container == "" {
			return base
		}
		return base + "/" + container
	}

	if raw, ok := values[TagDebug]; ok {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true", "1", "yes":
			spec.Tagged = true
		case "false", "0", "no":
		default:
			problems = append(problems, Problem{Key: keyFor(TagDebug), Value: raw,
				Reason: "the value is not a boolean", Hint: "use true or false"})
		}
	}
	if raw, ok := values[TagPort]; ok {
		port, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || port < 1 || port > 65535 {
			problems = append(problems, Problem{Key: keyFor(TagPort), Value: raw,
				Reason: "the value is not a TCP port", Hint: "use a number from 1 to 65535, for example 9229"})
		} else {
			spec.Tagged = true
			spec.Port = port
		}
	}
	if raw, ok := values[TagProtocol]; ok {
		name := strings.ToLower(strings.TrimSpace(raw))
		if _, known := Default.Lookup(name); known {
			spec.Protocol = name
		} else {
			problems = append(problems, Problem{Key: keyFor(TagProtocol), Value: raw,
				Reason: "no such protocol", Hint: "use one of " + strings.Join(Default.Names(), ", ")})
		}
	}
	raw, ok := values[TagSourcePath]
	key := TagSourcePath
	if !ok {
		raw, ok = values[TagHotReloadPath]
		key = TagHotReloadPath
	}
	if ok {
		normalised, err := hostpath.Normalize(raw)
		if err != nil {
			// Hot reload reports its own tag; a second warning for the same
			// value from here would only be noise.
			if key == TagSourcePath {
				problems = append(problems, Problem{Key: keyFor(key), Value: raw,
					Reason: "the path must be absolute", Hint: err.Error()})
			}
		} else {
			spec.SourcePath = normalised
			spec.SourcePathRaw = strings.TrimSpace(raw)
		}
	}
	return spec, problems
}

func firstOr(list []string, fallback string) string {
	if len(list) > 0 {
		return list[0]
	}
	return fallback
}

// firstKey is deterministic so a warning about a set of tags reads the same
// on every run.
func firstKey(values tagValues) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys[0]
}
