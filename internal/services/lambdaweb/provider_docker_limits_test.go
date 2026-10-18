package lambdaweb

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
)

func TestCodeTar_refusesMoreEntriesThanTheLimit(t *testing.T) {
	// Given: a small archive of empty files, one over the entry limit
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := range providerMaxPackageEntries + 1 {
		_, err := zw.Create(fmt.Sprintf("f%d", i))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	// When / Then: it is refused before anything is inflated
	_, err := codeTar(buf.Bytes())
	assert.ErrorIs(t, err, errPackageTooManyEntries)
}

func TestCodeTar_countsHeaderBytesTowardTheSizeLimit(t *testing.T) {
	// Given: entries whose contents fit the byte limit but whose tar headers,
	// at 512 bytes each, do not
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	const entries = providerMaxPackageEntries
	const each = providerMaxPackageBytes/entries - 100 // content alone: under the limit
	require.Less(t, uint64(entries)*each, uint64(providerMaxPackageBytes))
	for i := range entries {
		h := &zip.FileHeader{Name: fmt.Sprintf("f%d", i), Method: zip.Store}
		h.UncompressedSize64 = each
		w, err := zw.CreateRaw(h)
		require.NoError(t, err)
		_ = w // the declared size is what is checked before inflating
	}
	require.NoError(t, zw.Close())

	// When / Then: the declared sizes plus headers are over the limit
	_, err := codeTar(buf.Bytes())
	assert.ErrorIs(t, err, errPackageTooLarge)
}

type gatedSource struct {
	live, peak atomic.Int32
	release    chan struct{}
	pkg        []byte
}

func (g *gatedSource) Resolve(_ context.Context, ref codeRef) (codeRef, int64, *protocol.AWSError) {
	return ref, 0, nil
}

func (g *gatedSource) Fetch(ctx context.Context, _ codeRef) ([]byte, *protocol.AWSError) {
	n := g.live.Add(1)
	defer g.live.Add(-1)
	for {
		p := g.peak.Load()
		if n <= p || g.peak.CompareAndSwap(p, n) {
			break
		}
	}
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	return g.pkg, nil
}

func TestDockerProvider_buildsAtMostAFewPackagesAtOnce(t *testing.T) {
	// Given: more revisions starting for the first time than there are build slots
	src := &gatedSource{release: make(chan struct{}), pkg: zipBytes(t, map[string]string{"index.js": "x"})}
	p := NewDockerProvider(&config.Config{LambdaTarCacheMB: 64}, zap.NewNop(), nil)
	p.InitCode(src)
	var wg sync.WaitGroup
	for i := range 3 * maxConcurrentBuilds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, fail := p.buildTar(t.Context(), codeRef{Bucket: "b", Key: fmt.Sprintf("k%d", i), VersionID: "v"})
			assert.Nil(t, fail)
		}()
	}

	// When: fetches are held open
	require.Eventually(t, func() bool { return src.live.Load() == int32(maxConcurrentBuilds) }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	// Then: no more than the limit are in flight, and all finish once released
	assert.EqualValues(t, maxConcurrentBuilds, src.live.Load())
	close(src.release)
	wg.Wait()
	assert.LessOrEqual(t, src.peak.Load(), int32(maxConcurrentBuilds))
}
