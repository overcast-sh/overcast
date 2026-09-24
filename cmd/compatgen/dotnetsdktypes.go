//go:build dev

package main

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Reading the AWS SDK for .NET's own member types — the dotnet counterpart of
// gosdktypes.go (#1831).
//
// The dotnet-sdk backend emits C#, so it has to spell each request member the
// way AWSSDK declares it, and the pinned Smithy snapshot does not always say
// how that is. For most members the two agree, but AWSSDK customizes some:
// AWSSDK.CloudWatchLogs types InputLogEvent.Timestamp and LogStream.CreationTime
// as DateTime? where the model says long (epoch milliseconds), so a long
// written from the model does not compile (CS0029).
//
// go-sdk answers the same question by loading the vendored Go SDK at emit time.
// That route is closed here: the emitter is a Go program, `compatgen -check`
// runs in CI's docs job with no .NET SDK and no NuGet cache, and it must stay
// offline and byte-reproducible. So the dotnet-sdk suite reflects its own
// pinned assemblies into a text table (Scenario/SdkTypeTable.cs), one file per
// package under compat/suites/dotnet-sdk/sdk-types/, and the table is committed.
// This file reads it.
//
// Two guards keep the committed table the SDK's own:
//
//   - here, offline: every AWSSDK package OvercastCompat.csproj pins has a table
//     file of exactly that version, and no file names a package it does not
//     pin (checkDotnetSDKPins). A pin bump that forgot the table fails
//     generation and -check alike, naming the refresh command.
//   - in the suite: SdkTypeTableTests renders the table from the assemblies the
//     suite compiles against and fails on any difference from the committed
//     files — in the suite's image build and in CI's compat-suite-unit-tests
//     job, from a full checkout.
//
// A digest of the files in the style of models/aws/VERSION would add nothing to
// the second: it proves a file is what some run wrote, where the suite's test
// proves it is what the pinned assemblies declare.

// dotnetSDKTypesDir is where the table is committed, repository-relative.
const dotnetSDKTypesDir = "compat/suites/dotnet-sdk/sdk-types"

// dotnetCsprojPath is the project whose pins the table must match.
const dotnetCsprojPath = "compat/suites/dotnet-sdk/OvercastCompat.csproj"

// dotnetSDKTypesRefresh is the command that rewrites the table, quoted in every
// error that asks for it.
const dotnetSDKTypesRefresh = "docker build -f compat/suites/dotnet-sdk/Dockerfile --target sdk-types " +
	"--output type=local,dest=compat/suites/dotnet-sdk/sdk-types compat/suites"

// dotnetSDKTypes is the whole table: one package per AWSSDK service assembly.
type dotnetSDKTypes struct {
	// byNamespace is keyed by the service namespace, without its .Model
	// suffix — Amazon.CloudWatchLogs — which is what dotnetNameNamespace
	// derives from an SDK id.
	byNamespace map[string]*dotnetSDKPackage
	packages    []*dotnetSDKPackage
}

// dotnetSDKPackage is one AWSSDK package's model classes.
type dotnetSDKPackage struct {
	// Package and Version are the NuGet id and version the file was reflected
	// from: AWSSDK.CloudWatchLogs 4.0.0.
	Package string
	Version string
	// Namespace is the model namespace, Amazon.CloudWatchLogs.Model.
	Namespace string
	classes   map[string]map[string]dotnetType
	file      string
}

// class returns one model class's properties, keyed by property name.
func (p *dotnetSDKPackage) class(name string) (map[string]dotnetType, bool) {
	c, ok := p.classes[name]
	return c, ok
}

// describe names the package and version for a message: AWSSDK.CloudWatchLogs
// 4.0.0.
func (p *dotnetSDKPackage) describe() string { return p.Package + " " + p.Version }

// dotnetType is one property type, parsed from the table's closed grammar
// (Scenario/SdkTypeTable.cs's Spell):
//
//	string bool byte sbyte short ushort int uint long ulong float double decimal
//	DateTime MemoryStream Stream Document   each optionally Nullable (a trailing ?)
//	enum:<Name>                            an AWSSDK ConstantClass
//	class:<Name>                           a class in the package's model namespace
//	List<T>  Dictionary<K,V>               the SDK's two collections
//	type:<anything>                        verbatim, for a reader; never spelled
type dotnetType struct {
	// Form is one of "scalar", "enum", "class", "list", "map" or "other".
	Form string
	// Name is the scalar keyword, or the enum or class name.
	Name     string
	Nullable bool
	// Elem is a list's element type; Key and Value a map's.
	Elem, Key, Value *dotnetType
	// Raw is the token as the table spells it, for messages.
	Raw string
}

// scalar reports whether t is the named scalar, nullable or not.
func (t dotnetType) scalar(name string) bool { return t.Form == "scalar" && t.Name == name }

// parseDotnetType parses one type token.
func parseDotnetType(raw string) (dotnetType, error) {
	t := dotnetType{Raw: raw}
	body := raw
	if strings.HasPrefix(body, "type:") {
		t.Form, t.Name = "other", strings.TrimPrefix(body, "type:")
		return t, nil
	}
	if strings.HasSuffix(body, "?") {
		t.Nullable = true
		body = strings.TrimSuffix(body, "?")
	}
	switch {
	case strings.HasPrefix(body, "enum:"):
		t.Form, t.Name = "enum", strings.TrimPrefix(body, "enum:")
	case strings.HasPrefix(body, "class:"):
		t.Form, t.Name = "class", strings.TrimPrefix(body, "class:")
	case strings.HasPrefix(body, "List<") && strings.HasSuffix(body, ">"):
		args, err := splitDotnetTypeArgs(body[len("List<") : len(body)-1])
		if err != nil || len(args) != 1 {
			return t, fmt.Errorf("malformed list type %q", raw)
		}
		elem, err := parseDotnetType(args[0])
		if err != nil {
			return t, err
		}
		t.Form, t.Elem = "list", &elem
	case strings.HasPrefix(body, "Dictionary<") && strings.HasSuffix(body, ">"):
		args, err := splitDotnetTypeArgs(body[len("Dictionary<") : len(body)-1])
		if err != nil || len(args) != 2 {
			return t, fmt.Errorf("malformed dictionary type %q", raw)
		}
		key, err := parseDotnetType(args[0])
		if err != nil {
			return t, err
		}
		value, err := parseDotnetType(args[1])
		if err != nil {
			return t, err
		}
		t.Form, t.Key, t.Value = "map", &key, &value
	default:
		if !dotnetScalars[body] {
			return t, fmt.Errorf("unknown type %q", raw)
		}
		t.Form, t.Name = "scalar", body
	}
	return t, nil
}

// dotnetScalars is the scalar vocabulary of the table's grammar.
var dotnetScalars = map[string]bool{
	"string": true, "bool": true, "byte": true, "sbyte": true, "short": true, "ushort": true,
	"int": true, "uint": true, "long": true, "ulong": true, "float": true, "double": true,
	"decimal": true, "DateTime": true, "MemoryStream": true, "Stream": true, "Document": true,
}

// splitDotnetTypeArgs splits a generic argument list at its top-level commas.
func splitDotnetTypeArgs(s string) ([]string, error) {
	var args []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced %q", s)
			}
		case ',':
			if depth == 0 {
				args = append(args, s[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced %q", s)
	}
	return append(args, s[start:]), nil
}

// loadDotnetSDKTypes reads every table file in dir.
func loadDotnetSDKTypes(dir string) (*dotnetSDKTypes, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read the .NET SDK type table: %w (refresh it with `%s`)", err, dotnetSDKTypesRefresh)
	}
	types := &dotnetSDKTypes{byNamespace: map[string]*dotnetSDKPackage{}}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".txt" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		pkg, err := parseDotnetSDKPackage(entry.Name(), contents)
		if err != nil {
			return nil, err
		}
		service := strings.TrimSuffix(pkg.Namespace, ".Model")
		if prior, dup := types.byNamespace[service]; dup {
			return nil, fmt.Errorf("%s and %s both declare namespace %s", prior.file, pkg.file, pkg.Namespace)
		}
		types.byNamespace[service] = pkg
		types.packages = append(types.packages, pkg)
	}
	sort.Slice(types.packages, func(i, j int) bool { return types.packages[i].Package < types.packages[j].Package })
	return types, nil
}

// parseDotnetSDKPackage parses one table file.
func parseDotnetSDKPackage(file string, contents []byte) (*dotnetSDKPackage, error) {
	pkg := &dotnetSDKPackage{classes: map[string]map[string]dotnetType{}, file: file}
	var current map[string]dotnetType
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s/%s:%d: %s", dotnetSDKTypesDir, file, line, fmt.Sprintf(format, args...))
	}
	for scanner.Scan() {
		line++
		text := strings.TrimSuffix(scanner.Text(), "\r")
		switch {
		case text == "" || strings.HasPrefix(text, "#"):
			continue
		case strings.HasPrefix(text, "  "):
			if current == nil {
				return nil, fail("a property outside any class")
			}
			name, raw, ok := strings.Cut(strings.TrimPrefix(text, "  "), " ")
			if !ok {
				return nil, fail("want `  <Property> <type>`, got %q", text)
			}
			t, err := parseDotnetType(raw)
			if err != nil {
				return nil, fail("%v", err)
			}
			current[name] = t
		default:
			keyword, rest, _ := strings.Cut(text, " ")
			switch keyword {
			case "package":
				id, version, ok := strings.Cut(rest, " ")
				if !ok || pkg.Package != "" {
					return nil, fail("want one `package <id> <version>`")
				}
				pkg.Package, pkg.Version = id, version
			case "namespace":
				if pkg.Namespace != "" {
					return nil, fail("a second namespace")
				}
				pkg.Namespace = rest
			case "class":
				if _, dup := pkg.classes[rest]; dup {
					return nil, fail("class %s twice", rest)
				}
				current = map[string]dotnetType{}
				pkg.classes[rest] = current
			default:
				return nil, fail("unknown line %q", text)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if pkg.Package == "" || pkg.Namespace == "" {
		return nil, fmt.Errorf("%s/%s: no package or namespace header", dotnetSDKTypesDir, file)
	}
	if want := pkg.Package + ".txt"; file != want {
		return nil, fmt.Errorf("%s/%s: declares package %s, so it should be named %s", dotnetSDKTypesDir, file, pkg.Package, want)
	}
	return pkg, nil
}

// service returns the table for an SDK id's namespace, or an error naming what
// to pin.
func (t *dotnetSDKTypes) service(sdkID string) (*dotnetSDKPackage, error) {
	ns := dotnetNameNamespace(sdkID)
	if pkg, ok := t.byNamespace[ns]; ok {
		return pkg, nil
	}
	return nil, fmt.Errorf("no AWSSDK package in %s declares namespace %s: pin the service's package in %s and refresh the table with `%s`",
		dotnetSDKTypesDir, ns, dotnetCsprojPath, dotnetSDKTypesRefresh)
}

// csprojPackageReferences is the slice of an MSBuild project this reads.
type csprojPackageReferences struct {
	ItemGroups []struct {
		PackageReferences []struct {
			Include string `xml:"Include,attr"`
			Version string `xml:"Version,attr"`
		} `xml:"PackageReference"`
	} `xml:"ItemGroup"`
}

// checkDotnetSDKPins proves the table was reflected from the packages the suite
// pins: one file per AWSSDK service package the csproj references, at exactly
// its pinned version, and none for a package it does not. AWSSDK.Core declares
// no service and has no file.
func checkDotnetSDKPins(types *dotnetSDKTypes, csproj []byte) error {
	var project csprojPackageReferences
	if err := xml.Unmarshal(csproj, &project); err != nil {
		return fmt.Errorf("read %s: %w", dotnetCsprojPath, err)
	}
	pinned := map[string]string{}
	for _, group := range project.ItemGroups {
		for _, ref := range group.PackageReferences {
			if strings.HasPrefix(ref.Include, "AWSSDK.") && ref.Include != "AWSSDK.Core" {
				pinned[ref.Include] = ref.Version
			}
		}
	}
	var problems []string
	have := map[string]bool{}
	for _, pkg := range types.packages {
		have[pkg.Package] = true
		want, ok := pinned[pkg.Package]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s is in the table but %s does not reference it", pkg.Package, dotnetCsprojPath))
		case want != pkg.Version:
			problems = append(problems, fmt.Sprintf("%s is pinned at %s but the table was reflected from %s", pkg.Package, want, pkg.Version))
		}
	}
	for _, id := range sortedStringKeys(pinned) {
		if !have[id] {
			problems = append(problems, fmt.Sprintf("%s %s is pinned but has no table file", id, pinned[id]))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("the .NET SDK type table under %s does not match the pins in %s: %s; refresh it with `%s`",
			dotnetSDKTypesDir, dotnetCsprojPath, strings.Join(problems, "; "), dotnetSDKTypesRefresh)
	}
	return nil
}

// loadCheckedDotnetSDKTypes reads the table under root and checks it against
// the csproj's pins.
func loadCheckedDotnetSDKTypes(root string) (*dotnetSDKTypes, error) {
	types, err := loadDotnetSDKTypes(filepath.Join(root, filepath.FromSlash(dotnetSDKTypesDir)))
	if err != nil {
		return nil, err
	}
	csproj, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dotnetCsprojPath)))
	if err != nil {
		return nil, err
	}
	if err := checkDotnetSDKPins(types, csproj); err != nil {
		return nil, err
	}
	return types, nil
}
