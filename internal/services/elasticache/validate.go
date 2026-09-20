package elasticache

// validate.go — the create-time parameter rules the two classic create
// operations share, in one place so the Query form path and the typed path
// cannot drift apart (#144).
//
// Every constraint here is one the pinned ElastiCache model states in its own
// member documentation, not one inferred from behaviour:
//
//   - CacheClusterId / ReplicationGroupId: "A name must contain from 1 to 50
//     [40 for a replication group] alphanumeric characters or hyphens. The
//     first character must be a letter. A name cannot end with a hyphen or
//     contain two consecutive hyphens." Both are "stored as a lowercase
//     string", which is why a create canonicalises rather than refusing a
//     mixed-case id.
//   - NumCacheNodes: "For clusters running Valkey or Redis OSS, this value
//     must be 1. For clusters running Memcached, this value must be between 1
//     and 40."
//   - AZMode: the enum is single-az | cross-az, and "This parameter is only
//     supported for Memcached clusters."
//   - PreferredAvailabilityZones: "This option is only supported on
//     Memcached... The number of Availability Zones listed must equal the
//     value of NumCacheNodes."
//   - CreateReplicationGroup's Engine: "The value must be set to valkey or
//     redis."

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
)

const (
	// maxCacheClusterIDLen and maxReplicationGroupIDLen are the documented
	// identifier caps. They differ, and a replication group's is the tighter
	// of the two — a name legal for a cluster is not necessarily legal for a
	// group.
	maxCacheClusterIDLen     = 50
	maxReplicationGroupIDLen = 40

	// maxMemcachedCacheNodes is the per-cluster node cap AWS applies before a
	// limit-increase request. Valkey and Redis OSS clusters take exactly one.
	maxMemcachedCacheNodes = 40

	azModeSingle = "single-az"
	azModeCross  = "cross-az"
)

// ecCanonicalID is the form an ElastiCache identifier is stored in. Both
// CacheClusterId and ReplicationGroupId are documented as "stored as a
// lowercase string", so a caller that creates `MyCache` and describes
// `mycache` is talking about one cluster — which means every path that keys a
// record, a lock or a scheduler scope by an identifier must canonicalise it
// first, not just the create.
func ecCanonicalID(id string) string { return strings.ToLower(id) }

// errInvalidParameterCombination is the second of the two validation faults
// these operations model. AWS separates them: a value that is wrong on its own
// is InvalidParameterValue, a value that is wrong only because of another
// parameter is InvalidParameterCombination, and SDK callers branch on the
// difference.
func errInvalidParameterCombination(msg string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidParameterCombination",
		Message:    msg,
		HTTPStatus: http.StatusBadRequest,
	}
}

// validateCacheIdentifier applies the shared identifier grammar. param names
// the request parameter so the message says which one was refused, as AWS's
// does.
func validateCacheIdentifier(param, id string, maxLen int) *protocol.AWSError {
	invalid := func() *protocol.AWSError {
		return errInvalidParameterValue(fmt.Sprintf(
			"The parameter %s is not a valid identifier. Identifiers must contain from 1 to %d "+
				"alphanumeric characters or hyphens, must begin with a letter, and must not end "+
				"with a hyphen or contain two consecutive hyphens.", param, maxLen))
	}
	if id == "" || len(id) > maxLen {
		return invalid()
	}
	if !isASCIILetter(id[0]) {
		return invalid()
	}
	if id[len(id)-1] == '-' {
		return invalid()
	}
	if strings.Contains(id, "--") {
		return invalid()
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if isASCIILetter(c) || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return invalid()
	}
	return nil
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// validateCacheClusterEngine accepts the three engines a standalone cluster
// can run. Valkey is not in the model's own "memcached | redis" list, which
// predates it; it is accepted here because ElastiCache does run it and
// Overcast starts a Valkey container for it.
func validateCacheClusterEngine(engine string) *protocol.AWSError {
	switch engine {
	case "redis", "valkey", "memcached":
		return nil
	}
	return errInvalidParameterValue("Engine must be redis, valkey, or memcached")
}

// validateReplicationGroupEngine is the narrower list a replication group
// takes. Memcached does not replicate, and a group that accepted it would
// quietly start a Redis container instead of the engine the caller asked for.
func validateReplicationGroupEngine(engine string) *protocol.AWSError {
	switch engine {
	case "redis", "valkey":
		return nil
	}
	return errInvalidParameterValue("Engine must be redis or valkey for a replication group")
}

// validateNumCacheNodes takes the resolved node count — the default of 1 has
// already been applied — and checks it against the engine's own range.
func validateNumCacheNodes(engine string, numNodes int) *protocol.AWSError {
	if engine == "memcached" {
		if numNodes < 1 || numNodes > maxMemcachedCacheNodes {
			return errInvalidParameterValue(fmt.Sprintf(
				"NumCacheNodes must be between 1 and %d for a Memcached cluster.", maxMemcachedCacheNodes))
		}
		return nil
	}
	if numNodes != 1 {
		return errInvalidParameterValue(
			"NumCacheNodes must be 1 for a Valkey or Redis OSS cluster.")
	}
	return nil
}

// validateCachePlacement checks AZMode and PreferredAvailabilityZones, both of
// which are Memcached-only and are the only parameters here whose validity
// depends on another one.
func validateCachePlacement(engine, azMode string, zones []string, numNodes int) *protocol.AWSError {
	if azMode != "" {
		if azMode != azModeSingle && azMode != azModeCross {
			return errInvalidParameterValue(fmt.Sprintf(
				"AZMode must be %s or %s.", azModeSingle, azModeCross))
		}
		if engine != "memcached" {
			return errInvalidParameterCombination(
				"AZMode is only supported for Memcached clusters.")
		}
	}
	if len(zones) > 0 {
		if engine != "memcached" {
			return errInvalidParameterCombination(
				"PreferredAvailabilityZones is only supported for Memcached clusters.")
		}
		if len(zones) != numNodes {
			return errInvalidParameterCombination(
				"The number of Availability Zones in PreferredAvailabilityZones must equal NumCacheNodes.")
		}
	}
	return nil
}
