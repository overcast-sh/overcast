// Package awsmodel reads and parses a pinned checkout of AWS's public
// aws/api-models-aws Smithy JSON AST models into operation metadata.
//
// It exists to be shared by the tools that turn those models into Overcast
// build-time artifacts: cmd/awsmodelgen, which renders the parsed operations
// into internal/awsapi's generated manifest and routing indexes, and (per
// docs/plans/compat-coverage-modelgen.md §3.7) cmd/compatgen, which will use
// the same reader to derive compat test coverage. Both tools validate the
// checkout against the revision pinned in models/aws/VERSION before reading
// it; this package performs that validation but does not itself parse
// VERSION — callers pass the expected revision, exactly as
// `make generate-aws-operations` extracts it from the file today.
//
// This package only reads and parses models. It does not vendor them, cache
// them, or produce any generator output — emission is the caller's job.
package awsmodel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Operation is a single AWS API operation extracted from a Smithy service
// model, carrying the wire-protocol metadata needed to identify and route it.
type Operation struct {
	Service, ServiceShape, SDKID, APIVersion, Name, Protocol, TargetPrefix, SigningName, HTTPMethod, URI string
	Protocols                                                                                            []string
	// NoErrorWrapping records the restXml protocol trait's noErrorWrapping
	// flag: the service answers errors in a bare <Error>, not the
	// <ErrorResponse> envelope rest-xml otherwise wraps them in.
	NoErrorWrapping bool
}

// VerifyRevision checks that the Smithy model checkout at modelsDir — the
// models/ subdirectory of an api-models-aws clone — is checked out at
// exactly the given revision. It shells out to git in modelsDir's parent, so
// modelsDir must sit inside a git working tree; a branch name, a stale
// checkout, or any other commit is rejected.
func VerifyRevision(modelsDir, revision string) error {
	cmd := exec.Command("git", "-C", filepath.Dir(modelsDir), "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read model checkout revision: %w", err)
	}
	if got := strings.TrimSpace(string(output)); got != revision {
		return fmt.Errorf("model checkout revision %s does not match -source-revision %s", got, revision)
	}
	return nil
}

// LoadOperations walks modelsDir for Smithy JSON AST files (*.json) and
// returns every service operation they declare, sorted deterministically by
// service, operation name, API version, and URI. It returns an error if
// modelsDir contains no service operations at all.
func LoadOperations(modelsDir string) ([]Operation, error) {
	var operations []Operation
	err := filepath.WalkDir(modelsDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		parsed, err := ParseModel(path)
		if err != nil {
			return err
		}
		operations = append(operations, parsed...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read models: %w", err)
	}
	if len(operations) == 0 {
		return nil, fmt.Errorf("no service operations found in %s", modelsDir)
	}
	sort.SliceStable(operations, func(i, j int) bool {
		if operations[i].Service != operations[j].Service {
			return operations[i].Service < operations[j].Service
		}
		if operations[i].Name != operations[j].Name {
			return operations[i].Name < operations[j].Name
		}
		if operations[i].APIVersion != operations[j].APIVersion {
			return operations[i].APIVersion < operations[j].APIVersion
		}
		return operations[i].URI < operations[j].URI
	})
	return operations, nil
}
