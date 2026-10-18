package lambdaweb

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerlogs"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/protocol"
)

func TestNewDockerProvider_touchesNothing(t *testing.T) {
	// Given: a config and no Docker
	// When: the provider is constructed
	p := NewDockerProvider(testConfig(), nil, nil)

	// Then: it holds no client and no GC (nothing was probed or started)
	dc, gc := p.client()
	assert.Nil(t, dc)
	assert.Nil(t, gc)
}

func TestDockerProvider_startWithoutDocker(t *testing.T) {
	// Given: a provider that was never given Docker
	p := NewDockerProvider(testConfig(), nil, nil)

	// When: an environment is started
	env, fail := p.Start(context.Background(), envSpec{})

	// Then: it is an internal failure, not a panic
	assert.Nil(t, env)
	require.NotNil(t, fail)
	assert.Empty(t, fail.Type)
}

func TestDockerProvider_createRequestShape(t *testing.T) {
	// Given: a provider on a fake daemon and a revision
	f := newProviderFixture(t)

	// When: an environment is started
	env, fail := f.provider.Start(t.Context(), f.spec())
	require.Nil(t, fail)
	require.NotNil(t, env)

	// Then: the container runs the runtime's Lambda image under the bootstrap
	f.daemon.mu.Lock()
	creates := append([]docker.CreateContainerRequest(nil), f.daemon.creates...)
	f.daemon.mu.Unlock()
	require.Len(t, creates, 1)
	req := creates[0]
	assert.Equal(t, "public.ecr.aws/lambda/nodejs:24", req.Image)
	assert.Equal(t, []string{"/var/lang/bin/node", "/opt/overcast-web/bootstrap.mjs"}, req.Entrypoint)
	assert.Equal(t, "/var/task", req.WorkingDir)
	assert.Contains(t, req.ExposedPorts, "8080/tcp")

	// And: the environment contract is set next to the user's variables
	env1 := envMap(req.Env)
	assert.Equal(t, "hi", env1["GREETING"])
	assert.Equal(t, "0.0.0.0:8080", env1["AWS_LAMBDA_HTTP_ENDPOINT"])
	assert.Equal(t, "us-east-1", env1["AWS_REGION"])
	assert.Equal(t, "us-east-1", env1["AWS_DEFAULT_REGION"])
	assert.Equal(t, "my-fn", env1["AWS_LAMBDA_FUNCTION_NAME"])
	assert.Equal(t, "2048", env1["AWS_LAMBDA_FUNCTION_MEMORY_SIZE"])
	assert.Equal(t, "/var/task", env1["LAMBDA_TASK_ROOT"])
	assert.Equal(t, "--max-old-space-size=1024", env1["NODE_OPTIONS"])
	assert.Equal(t, "INFO", env1["OVERCAST_WEB_APP_LOG_LEVEL"])
	assert.NotEmpty(t, env1["AWS_ENDPOINT_URL"])
	assert.NotEmpty(t, env1["AWS_ACCESS_KEY_ID"])
	assert.NotEmpty(t, env1["AWS_SESSION_TOKEN"])

	// And: limits, no swap, two CPUs, a loopback published port, the control plane
	hc := req.HostConfig
	require.NotNil(t, hc)
	assert.Equal(t, int64(2048<<20), hc.Memory)
	assert.Equal(t, hc.Memory, hc.MemorySwap)
	assert.Equal(t, int64(2_000_000_000), hc.NanoCPUs)
	assert.Equal(t, "overcast_unit_control", hc.NetworkMode)
	require.Len(t, hc.PortBindings["8080/tcp"], 1)
	assert.Equal(t, "127.0.0.1", hc.PortBindings["8080/tcp"][0].HostIP)
	assert.Equal(t, "0", hc.PortBindings["8080/tcp"][0].HostPort)

	// And: the labels keep Lambda's GC off it
	assert.Equal(t, "true", req.Labels["overcast.managed"])
	assert.Equal(t, "lambdaweb", req.Labels["overcast.service"])
	assert.Equal(t, "my-fn", req.Labels["overcast.resource-id"])
	assert.NotEmpty(t, req.Labels["overcast.instance"])
	assert.NotEmpty(t, req.Labels[labelRun])

	// And: the data plane was joined, the address is the published port
	assert.True(t, f.daemon.sawRequest("POST /networks/overcast_unit/connect"), "requests: %v", f.daemon.requests)
	assert.Equal(t, "127.0.0.1:"+itoa(f.daemon.port), env.Addr())
}

func TestDockerProvider_userNodeOptionsWin(t *testing.T) {
	// Given: a revision that sets NODE_OPTIONS and TZ itself
	f := newProviderFixture(t)
	spec := f.spec()
	spec.Env = map[string]string{"NODE_OPTIONS": "--enable-source-maps", "TZ": "UTC"}

	// When: the environment is built
	got := envMap(f.provider.buildEnv(spec, f.provider.endpointMapper(t.Context(), mustClient(f))))

	// Then: the user's values stand, and nothing duplicates them
	assert.Equal(t, "--enable-source-maps", got["NODE_OPTIONS"])
	assert.Equal(t, "UTC", got["TZ"])
	assert.Equal(t, "en_US.UTF-8", got["LANG"])
}

func TestDockerProvider_runtimeVariablesBeatUserVariables(t *testing.T) {
	// Given: a user variable that shadows a runtime one
	f := newProviderFixture(t)
	spec := f.spec()
	spec.Env = map[string]string{"AWS_REGION": "mars-1"}

	// When: the environment is built
	got := envMap(f.provider.buildEnv(spec, f.provider.endpointMapper(t.Context(), mustClient(f))))

	// Then: the runtime's value wins
	assert.Equal(t, "us-east-1", got["AWS_REGION"])
}

func TestDockerProvider_injectsCodeAndBootstrapInOneArchive(t *testing.T) {
	// Given: a package with two files
	f := newProviderFixture(t)

	// When: an environment is started
	_, fail := f.provider.Start(t.Context(), f.spec())
	require.Nil(t, fail)

	// Then: one archive was copied, carrying var/task/** and the bootstrap
	f.daemon.mu.Lock()
	defer f.daemon.mu.Unlock()
	require.Len(t, f.daemon.archives, 1)
	entries := tarEntries(t, f.daemon.archives[0])
	assert.Equal(t, "// app", entries["var/task/index.js"])
	assert.Equal(t, "x", entries["var/task/lib/util.js"])
	assert.Equal(t, string(bootstrapSource), entries["opt/overcast-web/bootstrap.mjs"])
}

func TestDockerProvider_secondStartReusesTarAndImage(t *testing.T) {
	// Given: a revision started once
	f := newProviderFixture(t)
	_, fail := f.provider.Start(t.Context(), f.spec())
	require.Nil(t, fail)
	imageChecks := f.daemon.count("GET /images/")

	// When: it is started again
	_, fail = f.provider.Start(t.Context(), f.spec())
	require.Nil(t, fail)

	// Then: the package was fetched once, the image was not asked about again,
	// and the archive was cached by bucket/key/version
	assert.Equal(t, 1, f.src.fetched())
	assert.Equal(t, imageChecks, f.daemon.count("GET /images/"))
	entries, _ := f.provider.tars.size()
	assert.Equal(t, 1, entries)
	_, ok := f.provider.tars.get("b/app.zip/v1")
	assert.True(t, ok)
}

func TestDockerProvider_concurrentFirstStartsFetchOnce(t *testing.T) {
	// Given: a revision never started
	f := newProviderFixture(t)

	// When: several environments start at once
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, fail := f.provider.Start(t.Context(), f.spec())
			assert.Nil(t, fail)
		}()
	}
	wg.Wait()

	// Then: the package was fetched once and the image pulled at most once
	assert.Equal(t, 1, f.src.fetched())
	assert.LessOrEqual(t, f.daemon.pulls, 1)
}

func TestDockerProvider_imageRemovedBehindOurBack(t *testing.T) {
	// Given: a daemon that forgets the image once the provider verified it
	f := newProviderFixture(t)
	f.daemon.imageMissingOnce = true

	// When: an environment is started
	env, fail := f.provider.Start(t.Context(), f.spec())

	// Then: the create is retried after a pull
	require.Nil(t, fail)
	require.NotNil(t, env)
	assert.GreaterOrEqual(t, f.daemon.count("POST /images/create"), 1)
	assert.GreaterOrEqual(t, f.daemon.count("GET /images/"), 2, "the image was verified again after the create failed")
	assert.Equal(t, 2, f.daemon.count("POST /containers/create"))
}

func TestDockerProvider_exitDuringInitIsClassified(t *testing.T) {
	cases := []struct {
		name string
		code int
		oom  bool
		logs string
		want string
	}{
		{"import marker", 87, false, "OVERCAST_WEB_IMPORT_ERROR: Error: Cannot find module './x'\n", errTypeImportCode},
		{"init error", 88, false, "OVERCAST_WEB_INIT_ERROR: Error: boom\n", errTypeInitCode},
		{"out of memory", 137, true, "", errTypeOutOfMemory},
		{"uncaught exception", 1, false, "/var/task/index.js:3\n  throw e\n  ^\nError: late\n    at Object.<anonymous> (/var/task/index.js:3:9)\n\nNode.js v24.1.0\n", errTypeInitCode},
		{"plain exit", 2, false, "bye\n", errTypeExitError},
		{"clean exit", 0, false, "", errTypeExitError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a container whose process ends before it listens
			f := newProviderFixture(t)
			f.daemon.port = closedPort(t)
			f.daemon.logs = tc.logs
			go func() {
				// the process ends once the provider is waiting on it
				time.Sleep(60 * time.Millisecond)
				f.daemon.endProcess("ctra", tc.code, tc.oom)
			}()

			// When: it is started
			env, fail := f.provider.Start(t.Context(), f.spec())

			// Then: the failure is classified, quotes the output, and the container is removed
			require.Nil(t, env)
			require.NotNil(t, fail)
			assert.Equal(t, tc.want, fail.Type, fail.Detail)
			if tc.logs != "" {
				assert.Contains(t, fail.Detail, strings.SplitN(tc.logs, "\n", 2)[0])
			}
			assert.LessOrEqual(t, len(fail.Detail), 4096)
			f.daemon.mu.Lock()
			assert.Equal(t, []string{"ctra"}, f.daemon.deleted)
			f.daemon.mu.Unlock()
		})
	}
}

func TestDockerProvider_neverListeningTimesOut(t *testing.T) {
	// Given: a container that runs but whose port stays closed
	f := newProviderFixture(t)
	f.daemon.port = closedPort(t)
	f.daemon.logs = "booting...\n"
	spec := f.spec()
	spec.InitTimeout = 150 * time.Millisecond

	// When: it is started
	t0 := time.Now()
	env, fail := f.provider.Start(t.Context(), spec)

	// Then: it times out after the init timeout, with the output, and is removed
	require.Nil(t, env)
	require.NotNil(t, fail)
	assert.Equal(t, errTypeTimeoutListen, fail.Type)
	assert.Contains(t, fail.Detail, "booting...")
	assert.GreaterOrEqual(t, time.Since(t0), 150*time.Millisecond)
	assert.Equal(t, []string{"ctra"}, f.daemon.deleted)
}

func TestDockerProvider_cancelledStartLeavesNothing(t *testing.T) {
	// Given: a container that never listens and a caller that gives up
	f := newProviderFixture(t)
	f.daemon.port = closedPort(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	// When: the start is cancelled mid-init
	env, fail := f.provider.Start(ctx, f.spec())

	// Then: it fails without an error type and the container is removed
	require.Nil(t, env)
	require.NotNil(t, fail)
	assert.Empty(t, fail.Type)
	assert.Equal(t, []string{"ctra"}, f.daemon.deleted)
}

func TestDockerProvider_missingPackageFailsTheRevision(t *testing.T) {
	// Given: a package that is gone from S3
	f := newProviderFixture(t)
	f.src.err = &protocol.AWSError{Code: "NoSuchKey", Message: "gone"}

	// When: an environment is started
	env, fail := f.provider.Start(t.Context(), f.spec())

	// Then: the failure tells the engine the revision's code is unavailable
	require.Nil(t, env)
	require.NotNil(t, fail)
	assert.Equal(t, "Overcast.RevisionCodeUnavailable", fail.Type)
	assert.Empty(t, f.daemon.creates, "no container is created for a package we cannot get")
}

func TestDockerProvider_corruptPackage(t *testing.T) {
	// Given: a package that is not a zip
	f := newProviderFixture(t)
	f.src.pkgs["app.zip"] = []byte("not a zip")

	// When: an environment is started
	_, fail := f.provider.Start(t.Context(), f.spec())

	// Then: it is an internal failure naming the package
	require.NotNil(t, fail)
	assert.Empty(t, fail.Type)
	assert.Contains(t, fail.Detail, "invalid package")
}

func TestDockerProvider_unknownRuntime(t *testing.T) {
	// Given: a revision with a runtime that is not Node.js
	f := newProviderFixture(t)
	spec := f.spec()
	spec.Revision.BuildConfig.RuntimeConfig.Runtime = "python3.12"

	// When: an environment is started
	_, fail := f.provider.Start(t.Context(), spec)

	// Then: it is refused
	require.NotNil(t, fail)
	assert.Contains(t, fail.Detail, "not a Node.js runtime")
}

func TestDockerEnv_stopSendsSigtermWithGraceThenRemoves(t *testing.T) {
	// Given: a running environment
	f := newProviderFixture(t)
	env, fail := f.provider.Start(t.Context(), f.spec())
	require.Nil(t, fail)

	// When: it is stopped with a one second grace, by several callers at once
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, env.Stop(t.Context(), time.Second))
		}()
	}
	wg.Wait()
	assert.NoError(t, env.Stop(t.Context(), time.Second), "idempotent")

	// Then: the daemon was asked once to stop it with t=1 (SIGTERM, then SIGKILL),
	// the container is removed, Done is closed, and the exit was requested
	f.daemon.mu.Lock()
	defer f.daemon.mu.Unlock()
	assert.Equal(t, []string{"ctra?t=1"}, f.daemon.stops)
	assert.Equal(t, []string{"ctra"}, f.daemon.deleted)
	select {
	case <-env.Done():
	default:
		t.Fatal("Done is not closed")
	}
	assert.True(t, env.ExitReason().Requested)
	assert.False(t, env.ExitReason().OOM)
}

func TestDockerEnv_doneClosesForAnyExit(t *testing.T) {
	// Given: a running environment
	f := newProviderFixture(t)
	env, fail := f.provider.Start(t.Context(), f.spec())
	require.Nil(t, fail)

	// When: the process is killed for memory behind our back
	f.daemon.endProcess("ctra", 137, true)

	// Then: Done closes and the reason says out of memory, not requested
	select {
	case <-env.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done never closed")
	}
	assert.True(t, env.ExitReason().OOM)
	assert.Equal(t, 137, env.ExitReason().Code)
	assert.False(t, env.ExitReason().Requested)
}

func TestDockerEnv_stats(t *testing.T) {
	// Given: a running environment using 100 MiB of which 20 MiB is cache
	f := newProviderFixture(t)
	env, fail := f.provider.Start(t.Context(), f.spec())
	require.Nil(t, fail)

	// When: it is sampled
	s := env.Stats(t.Context())

	// Then: memory is the working set and CPU the cumulative time
	assert.InDelta(t, 80.0, s.MaxMemoryMB, 0.01)
	assert.Equal(t, 1500*time.Millisecond, s.CPUTime)
}

func TestDockerProvider_stopDrainsEnvironments(t *testing.T) {
	// Given: two running environments
	f := newProviderFixture(t)
	a, fail := f.provider.Start(t.Context(), f.spec())
	require.Nil(t, fail)
	b, fail := f.provider.Start(t.Context(), f.spec())
	require.Nil(t, fail)

	// When: the provider is stopped, as at shutdown
	f.provider.Stop(t.Context())

	// Then: both are done and removed
	for _, e := range []environment{a, b} {
		select {
		case <-e.Done():
		default:
			t.Fatalf("%s still running", e.ID())
		}
	}
	f.daemon.mu.Lock()
	defer f.daemon.mu.Unlock()
	assert.ElementsMatch(t, []string{"ctra", "ctrb"}, f.daemon.deleted)
}

func TestDockerProvider_applicationLogsUseTheirOwnTimestamp(t *testing.T) {
	// Given: a container that printed one application log line
	f := newProviderFixture(t)
	line := `{"timestamp":"2026-03-04T05:06:07.123Z","level":"INFO","message":"hello","requestId":"r1"}`
	f.daemon.streamLines = "2026-03-04T05:06:09.000000000Z " + line + "\n"
	f.provider.InitLogWriter(f.logs)

	// When: it is started and then stopped
	env, fail := f.provider.Start(t.Context(), f.spec())
	require.Nil(t, fail)
	require.Eventually(t, func() bool { return len(f.logs.snapshot()) > 0 }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, env.Stop(t.Context(), time.Second))

	// Then: it reached the environment's log stream once, stamped with the
	// application's time rather than the daemon's, and the stream was created
	entries := f.logs.snapshot()
	require.Len(t, entries, 1)
	assert.Equal(t, line, entries[0].Message)
	assert.Equal(t, time.Date(2026, 3, 4, 5, 6, 7, 123_000_000, time.UTC).UnixMilli(), entries[0].Timestamp)
	assert.Contains(t, f.logs.groups, "/aws/lambda/web/my-fn")
	assert.Contains(t, f.logs.streams, "/aws/lambda/web/my-fn|2026/01/01/[rev1]abc")
}

func TestAppLogSink_leavesOtherLinesAlone(t *testing.T) {
	// Given: a sink and a line that is not an application log
	var got containerlogs.Line
	sink := &appLogSink{inner: lineCatcher{&got}}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// When: it receives a plain line and a malformed one
	sink.Line(containerlogs.Line{Time: stamp, Message: "plain text"})
	assert.Equal(t, stamp, got.Time)
	sink.Line(containerlogs.Line{Time: stamp, Message: `{"timestamp":"nope","x":1}`})

	// Then: the daemon's time stands
	assert.Equal(t, stamp, got.Time)
}

type lineCatcher struct{ into *containerlogs.Line }

func (c lineCatcher) Line(l containerlogs.Line) bool { *c.into = l; return true }
func (c lineCatcher) Flush() int                     { return 0 }

func TestClassifyExit_tailIsBounded(t *testing.T) {
	// Given: far more output than a failure may quote
	tail := strings.Repeat("x", 10_000)

	// When: it is classified
	f := classifyExit(2, false, tail)

	// Then: the detail stays within 4 KiB
	assert.Equal(t, errTypeExitError, f.Type)
	assert.LessOrEqual(t, len(f.Detail), 4096)
}

func TestCodeTar(t *testing.T) {
	t.Run("drops entries that escape var/task", func(t *testing.T) {
		// Given: a zip with traversal and absolute names
		data := zipBytes(t, map[string]string{
			"ok.js": "1", "../evil": "2", "/abs": "3", "a/../../up": "4", "dir\\win.js": "5",
		})

		// When: it is converted
		got, err := codeTar(data)
		require.NoError(t, err)

		// Then: only safe entries are in the archive, under var/task
		entries := tarEntries(t, got)
		assert.Equal(t, "1", entries["var/task/ok.js"])
		assert.Equal(t, "5", entries["var/task/dir/win.js"])
		for name := range entries {
			assert.NotContains(t, name, "..")
			assert.NotContains(t, name, "evil")
			assert.NotContains(t, name, "abs")
		}
	})

	t.Run("keeps relative symlinks, drops escaping ones", func(t *testing.T) {
		// Given: a zip with a good and a bad symlink
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, target := range map[string]string{"bin/ok": "../lib/x.js", "bin/bad": "../../etc/passwd", "bin/abs": "/etc/passwd"} {
			h := &zip.FileHeader{Name: name}
			h.SetMode(0o777 | fs.ModeSymlink)
			w, err := zw.CreateHeader(h)
			require.NoError(t, err)
			_, _ = w.Write([]byte(target))
		}
		require.NoError(t, zw.Close())

		// When: it is converted
		got, err := codeTar(buf.Bytes())
		require.NoError(t, err)

		// Then: only the contained link survives
		links := map[string]string{}
		tr := tar.NewReader(bytes.NewReader(got))
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			if h.Typeflag == tar.TypeSymlink {
				links[h.Name] = h.Linkname
			}
		}
		assert.Equal(t, map[string]string{"var/task/bin/ok": "../lib/x.js"}, links)
	})

	t.Run("refuses a package over 512 MB unzipped", func(t *testing.T) {
		// Given: a zip whose entries declare 600 MB
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.CreateRaw(&zip.FileHeader{Name: "huge.bin", Method: zip.Store, UncompressedSize64: 600 << 20, CompressedSize64: 1})
		require.NoError(t, err)
		_, _ = w.Write([]byte{0})
		require.NoError(t, zw.Close())

		// When: it is converted
		_, err = codeTar(buf.Bytes())

		// Then: it is refused before anything is inflated
		assert.ErrorIs(t, err, errPackageTooLarge)
	})
}

func TestTarCache_evictsLeastRecentlyUsedPastTheBudget(t *testing.T) {
	// Given: a cache that holds 10 bytes
	c := newTarCache(10)
	c.put(&tarEntry{key: "a", data: make([]byte, 4)})
	c.put(&tarEntry{key: "b", data: make([]byte, 4)})
	_, _ = c.get("a") // a is now the most recent

	// When: a third entry does not fit
	c.put(&tarEntry{key: "c", data: make([]byte, 4)})

	// Then: b, the least recently used, is gone; an oversize entry is never kept
	_, hasA := c.get("a")
	_, hasB := c.get("b")
	_, hasC := c.get("c")
	assert.True(t, hasA)
	assert.False(t, hasB)
	assert.True(t, hasC)
	c.put(&tarEntry{key: "big", data: make([]byte, 11)})
	_, hasBig := c.get("big")
	assert.False(t, hasBig)
	var disabled *tarCache
	disabled.put(&tarEntry{key: "x"})
	_, ok := disabled.get("x")
	assert.False(t, ok)
}

func TestProbeListening(t *testing.T) {
	// Given: a quiet listener and a closed port
	open, closed := holdListener(t), closedPort(t)

	// When/Then: a server waiting for a request is ready through a published
	// port and directly; a closed port is neither
	assert.True(t, probeListening(t.Context(), "127.0.0.1:"+itoa(open), false))
	assert.True(t, probeListening(t.Context(), "127.0.0.1:"+itoa(open), true))
	assert.False(t, probeListening(t.Context(), "127.0.0.1:"+itoa(closed), false))
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func testConfig() *config.Config {
	return &config.Config{Region: "us-east-1", AccountID: "000000000000", Network: "overcast_unit", Port: 4566, LambdaTarCacheMB: 16}
}

func mustClient(f *providerFixture) *docker.Client {
	dc, _ := f.provider.client()
	return dc
}
