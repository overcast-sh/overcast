package elasticache

// endpoint_test.go — a cache endpoint is minted for whoever asks, on the terms
// docs/networking/data-plane-endpoints.md states for every data-plane name.
//
// Before endpoint.go the record was overwritten after the container started
// with the address Overcast itself dials, and every caller got that: a
// function discovering its cache through DescribeCacheClusters at runtime was
// handed 127.0.0.1 and connected to itself.

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/state"
)

func endpointTestHandler(cfg *config.Config) *Handler {
	return New(cfg, state.NewMemoryStore(), zap.NewNop(), clock.New()).handler
}

func siblingCtx(origin string) context.Context {
	return middleware.ContextWithClientAddr(
		middleware.ContextWithClientEndpoint(context.Background(), origin), "172.18.0.7")
}

func hostCtx(origin string) context.Context {
	return middleware.ContextWithClientAddr(
		middleware.ContextWithClientEndpoint(context.Background(), origin), "127.0.0.1")
}

// A running cluster's endpoint stays a hostname for a sibling container, on
// the base the caller reached Overcast on, with the engine's own port — the
// host port is not bound inside the network.
func TestClusterEndpoint_containerCallerGetsTheNameAndEnginePort(t *testing.T) {
	h := endpointTestHandler(&config.Config{Region: "ap-southeast-2", Port: 4566})
	c := &CacheCluster{
		CacheClusterId:        "sessions",
		Engine:                "redis",
		ConfigurationEndpoint: &ClusterEndpoint{Address: "sessions.ap-southeast-2.cfg.localhost", Port: 6379},
		DockerContainerID:     "abc123",
		HostPort:              63791,
		DialAddress:           "172.19.0.4",
		DialPort:              6379,
	}

	got := h.clusterEndpointFor(siblingCtx("http://localhost.overcast.sh:4566"), c)

	if want := "sessions.ap-southeast-2.cfg.localhost.overcast.sh"; got.Address != want {
		t.Errorf("Address = %q, want %q", got.Address, want)
	}
	if got.Port != 6379 {
		t.Errorf("Port = %d, want the engine port 6379", got.Port)
	}
}

// The host reaches the container only through its published port, and
// through a name it can resolve — or a loopback address when it cannot.
func TestClusterEndpoint_hostCallerGetsThePublishedPort(t *testing.T) {
	tests := []struct {
		name        string
		hostname    string
		wantAddress string
	}{
		{"wildcard DNS resolves the name", "localhost.overcast.sh", "sessions.us-east-1.cfg.localhost.overcast.sh"},
		{"bare localhost degrades to loopback", "", "127.0.0.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := endpointTestHandler(&config.Config{Region: "us-east-1", Port: 4566, Hostname: tc.hostname})
			c := &CacheCluster{
				CacheClusterId:        "sessions",
				Engine:                "redis",
				ConfigurationEndpoint: &ClusterEndpoint{Address: "sessions.us-east-1.cfg.localhost", Port: 6379},
				DockerContainerID:     "abc123",
				HostPort:              63791,
			}

			got := h.clusterEndpointFor(hostCtx("http://localhost:4566"), c)

			if got.Address != tc.wantAddress {
				t.Errorf("Address = %q, want %q", got.Address, tc.wantAddress)
			}
			if got.Port != 63791 {
				t.Errorf("Port = %d, want the published host port 63791", got.Port)
			}
		})
	}
}

// Before the container exists there is nothing dialable to offer, so every
// caller gets the AWS-shaped name and port. This is the value CloudFormation
// captures for RedisEndpoint.Address at create time and bakes into a task
// definition, so it must be a name the container will later answer to.
func TestClusterEndpoint_beforeTheContainerEveryCallerGetsTheName(t *testing.T) {
	h := endpointTestHandler(&config.Config{Region: "us-east-1", Port: 4566, Hostname: "localhost.overcast.sh"})
	c := &CacheCluster{
		CacheClusterId:        "sessions",
		Engine:                "memcached",
		ConfigurationEndpoint: &ClusterEndpoint{Address: "sessions.us-east-1.cfg.localhost.overcast.sh", Port: 11211},
	}

	for name, ctx := range map[string]context.Context{
		"host":    hostCtx("http://localhost.overcast.sh:4566"),
		"sibling": siblingCtx("http://localhost.overcast.sh:4566"),
	} {
		got := h.clusterEndpointFor(ctx, c)
		if want := "sessions.us-east-1.cfg.localhost.overcast.sh"; got.Address != want {
			t.Errorf("%s: Address = %q, want %q", name, got.Address, want)
		}
		if got.Port != 11211 {
			t.Errorf("%s: Port = %d, want the memcached port 11211", name, got.Port)
		}
	}
}

// State written by an older build carries Overcast's own dial target in the
// endpoint field. Rendering re-mints from the id, so the container IP never
// reaches a caller, and the health check still finds its target there.
func TestClusterEndpoint_recordOverwrittenByAnOlderBuildIsReminted(t *testing.T) {
	h := endpointTestHandler(&config.Config{Region: "us-east-1", Port: 4566, Hostname: "localhost.overcast.sh"})
	c := &CacheCluster{
		CacheClusterId:        "sessions",
		Engine:                "redis",
		ConfigurationEndpoint: &ClusterEndpoint{Address: "172.18.0.3", Port: 6379},
		DockerContainerID:     "abc123",
		HostPort:              63791,
	}

	got := h.clusterEndpointFor(siblingCtx("http://localhost.overcast.sh:4566"), c)
	if want := "sessions.us-east-1.cfg.localhost.overcast.sh"; got.Address != want {
		t.Errorf("Address = %q, want %q", got.Address, want)
	}

	if host, port := c.dialTarget(); host != "172.18.0.3" || port != 6379 {
		t.Errorf("dialTarget = %s:%d, want the stored 172.18.0.3:6379", host, port)
	}
}

// The dial target is the health check's, and the view a caller gets never
// writes it back into the record.
func TestCacheClusterForCaller_leavesTheRecordAlone(t *testing.T) {
	h := endpointTestHandler(&config.Config{Region: "us-east-1", Port: 4566})
	c := &CacheCluster{
		CacheClusterId:        "sessions",
		Engine:                "redis",
		ConfigurationEndpoint: &ClusterEndpoint{Address: "sessions.us-east-1.cfg.localhost", Port: 6379},
		DockerContainerID:     "abc123",
		HostPort:              63791,
		DialAddress:           "127.0.0.1",
		DialPort:              63791,
	}

	view := h.cacheClusterForCaller(hostCtx("http://localhost:4566"), c)

	if view.ConfigurationEndpoint.Address != "127.0.0.1" || view.ConfigurationEndpoint.Port != 63791 {
		t.Errorf("view endpoint = %+v, want the host's loopback dial pair", view.ConfigurationEndpoint)
	}
	if c.ConfigurationEndpoint.Address != "sessions.us-east-1.cfg.localhost" {
		t.Errorf("record endpoint = %q; the caller view leaked into the record", c.ConfigurationEndpoint.Address)
	}
	if host, port := c.dialTarget(); host != "127.0.0.1" || port != 63791 {
		t.Errorf("dialTarget = %s:%d, want 127.0.0.1:63791", host, port)
	}
}

// Replication groups and serverless caches mint on the same rule; each has
// its own name grammar.
func TestGroupAndServerlessEndpoints_mintedForTheCaller(t *testing.T) {
	h := endpointTestHandler(&config.Config{Region: "us-east-1", Port: 4566})
	ctx := siblingCtx("http://localhost.overcast.sh:4566")

	rg := &ReplicationGroup{
		ReplicationGroupId:    "app-cache",
		Engine:                "valkey",
		ConfigurationEndpoint: &ClusterEndpoint{Address: "app-cache.us-east-1.ng.cfg.localhost", Port: 6379},
		DockerContainerID:     "abc123",
		HostPort:              63792,
	}
	if got := h.replicationGroupEndpointFor(ctx, rg); got.Address != "app-cache.us-east-1.ng.cfg.localhost.overcast.sh" || got.Port != 6379 {
		t.Errorf("replication group endpoint = %+v", got)
	}

	sc := &ServerlessCache{
		ServerlessCacheName: "quick",
		Engine:              "redis",
		Endpoint:            &ClusterEndpoint{Address: "quick.us-east-1.serverless.localhost", Port: 6379},
		ReaderEndpoint:      &ClusterEndpoint{Address: "quick.us-east-1.serverless.localhost", Port: 6379},
		DockerContainerID:   "abc123",
		HostPort:            63793,
	}
	view := h.serverlessCacheForCaller(ctx, sc)
	if view.Endpoint.Address != "quick.us-east-1.serverless.localhost.overcast.sh" || view.Endpoint.Port != 6379 {
		t.Errorf("serverless endpoint = %+v", view.Endpoint)
	}
	if view.ReaderEndpoint.Address != view.Endpoint.Address {
		t.Errorf("reader endpoint = %+v, want the same node as the writer", view.ReaderEndpoint)
	}
}
