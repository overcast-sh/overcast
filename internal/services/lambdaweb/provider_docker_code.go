package lambdaweb

// provider_docker_code.go — a revision's package as the archive that goes into
// the container.
//
// One tar carries var/task/** (from the S3 zip) and the bootstrap, so a cold
// start provisions the filesystem with a single CopyToContainer. The tar is
// built once per pinned S3 version and kept in a byte-bounded LRU keyed
// bucket/key/versionId: the second environment of a revision never fetches or
// unzips anything.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// codeUnavailableType is the initFailure type for a package that is gone from
// S3; the engine turns it into 409 RevisionNotActive and fails the revision.
const codeUnavailableType = "Overcast.RevisionCodeUnavailable"

var errPackageTooLarge = errors.New("package exceeds 512 MB unzipped")

// tarKey identifies a pinned package version.
func tarKey(ref codeRef) string { return ref.Bucket + "/" + ref.Key + "/" + ref.VersionID }

// codeTar is the archive for one package: var/task from the zip plus the
// bootstrap. Entries that would land outside var/task are dropped.
func codeTar(zipData []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	// The declared sizes are checked before anything is inflated; the copy
	// below checks the actual ones, since a header can lie.
	var declared uint64
	for _, f := range zr.File {
		declared += f.UncompressedSize64
		if declared > maxPackageBytes {
			return nil, errPackageTooLarge
		}
	}

	const prefix = "var/task/"
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var total int64

	for _, f := range zr.File {
		name, ok := safeEntryName(f.Name)
		if !ok {
			continue
		}
		hdr := &tar.Header{Name: prefix + name, ModTime: f.Modified}
		mode := f.Mode()
		perm := int64(mode.Perm())

		switch {
		case f.FileInfo().IsDir():
			hdr.Typeflag, hdr.Mode = tar.TypeDir, 0o755
			hdr.Name += "/"
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, fmt.Errorf("tar header %q: %w", f.Name, err)
			}
		case mode&fs.ModeSymlink != 0:
			target, err := readSymlinkTarget(f)
			if err != nil {
				return nil, err
			}
			// A link that leaves var/task would expose the container's own
			// filesystem to the package; drop it.
			if path.IsAbs(target) || !within(path.Join(path.Dir(name), target)) {
				continue
			}
			hdr.Typeflag, hdr.Linkname, hdr.Mode = tar.TypeSymlink, target, 0o777
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, fmt.Errorf("tar header %q: %w", f.Name, err)
			}
		default:
			if perm&0o444 == 0 {
				perm |= 0o444
			}
			hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeReg, perm, int64(f.UncompressedSize64)
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, fmt.Errorf("tar header %q: %w", f.Name, err)
			}
			src, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("open %q in zip: %w", f.Name, err)
			}
			n, err := io.Copy(tw, io.LimitReader(src, hdr.Size+1))
			src.Close()
			if err != nil {
				return nil, fmt.Errorf("write %q to tar: %w", f.Name, err)
			}
			if n != hdr.Size {
				return nil, fmt.Errorf("%q does not match its declared size", f.Name)
			}
			total += n
			if total > maxPackageBytes {
				return nil, errPackageTooLarge
			}
		}
	}

	boot := &tar.Header{Name: strings.TrimPrefix(webBootstrapPath, "/"), Typeflag: tar.TypeReg, Mode: 0o444, Size: int64(len(bootstrapSource))}
	if err := tw.WriteHeader(boot); err != nil {
		return nil, err
	}
	if _, err := tw.Write(bootstrapSource); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	return buf.Bytes(), nil
}

// safeEntryName normalises a zip entry name to a relative path inside the
// package, or reports that it names something outside it (or the root itself).
func safeEntryName(name string) (string, bool) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimPrefix(name, "./")
	if name == "" || strings.HasPrefix(name, "/") {
		return "", false
	}
	clean := path.Clean(name)
	if clean == "." || !within(clean) {
		return "", false
	}
	return clean, true
}

// within reports whether a cleaned relative path stays below its root.
func within(p string) bool {
	return p != ".." && !strings.HasPrefix(p, "../") && !path.IsAbs(p)
}

func readSymlinkTarget(f *zip.File) (string, error) {
	r, err := f.Open()
	if err != nil {
		return "", fmt.Errorf("open %q in zip: %w", f.Name, err)
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return "", fmt.Errorf("read %q in zip: %w", f.Name, err)
	}
	return strings.ReplaceAll(string(b), "\\", "/"), nil
}

// codePrep is a package archive being prepared: ready already on a cache hit,
// otherwise delivered by the goroutine that fetches and converts it.
type codePrep struct {
	tar  []byte
	fail *initFailure
	ch   chan struct{}
}

func (c *codePrep) wait(ctx context.Context) ([]byte, *initFailure) {
	if c.ch != nil {
		select {
		case <-c.ch:
		case <-ctx.Done():
			return nil, internalFailure(ctx.Err().Error())
		}
	}
	return c.tar, c.fail
}

// prepareCode starts getting ref's archive. A cache hit costs no goroutine.
func (p *DockerProvider) prepareCode(ctx context.Context, ref codeRef) *codePrep {
	if e, ok := p.tars.get(tarKey(ref)); ok {
		return &codePrep{tar: e.data}
	}
	c := &codePrep{ch: make(chan struct{})}
	go func() {
		defer close(c.ch)
		c.tar, c.fail = p.buildTar(ctx, ref)
	}()
	return c
}

// buildFlights lets the concurrent first starts of a revision share one fetch.
type buildFlights struct {
	mu sync.Mutex
	m  map[string]*buildFlight
}

type buildFlight struct {
	done chan struct{}
	tar  []byte
	fail *initFailure
}

func (p *DockerProvider) buildTar(ctx context.Context, ref codeRef) ([]byte, *initFailure) {
	key := tarKey(ref)
	b := &p.builds
	b.mu.Lock()
	if e, ok := p.tars.get(key); ok {
		b.mu.Unlock()
		return e.data, nil
	}
	f, running := b.m[key]
	if !running {
		if b.m == nil {
			b.m = make(map[string]*buildFlight)
		}
		f = &buildFlight{done: make(chan struct{})}
		b.m[key] = f
		go p.runBuild(context.WithoutCancel(ctx), ref, key, f)
	}
	b.mu.Unlock()

	select {
	case <-f.done:
		return f.tar, f.fail
	case <-ctx.Done():
		return nil, internalFailure(ctx.Err().Error())
	}
}

func (p *DockerProvider) runBuild(ctx context.Context, ref codeRef, key string, f *buildFlight) {
	defer close(f.done)
	defer func() {
		p.builds.mu.Lock()
		delete(p.builds.m, key)
		p.builds.mu.Unlock()
	}()

	src := p.codeSource()
	if src == nil {
		f.fail = internalFailure("no code source is wired")
		return
	}
	data, aerr := src.Fetch(ctx, ref)
	if aerr != nil {
		f.fail = codeFetchFailure(aerr)
		return
	}
	tarData, err := codeTar(data)
	if err != nil {
		f.fail = internalFailure("invalid package: " + err.Error())
		return
	}
	f.tar = tarData
	p.tars.put(&tarEntry{key: key, data: tarData})
}

// codeFetchFailure maps a failed S3 read. A package that is gone means the
// revision can no longer run, which the engine reads off the type; anything
// else is the platform's.
func codeFetchFailure(aerr *protocol.AWSError) *initFailure {
	switch aerr.Code {
	case "NoSuchKey", "NoSuchVersion", "NoSuchBucket":
		return &initFailure{Type: codeUnavailableType, Detail: "the revision's package is no longer available: " + aerr.Message}
	}
	return internalFailure("fetch package: " + aerr.Code + ": " + aerr.Message)
}

// ─── Archive cache ───────────────────────────────────────────────────────────

type tarEntry struct {
	key  string
	data []byte
}

// tarCache is a mutex-guarded LRU bounded by total archive bytes. A nil cache
// is valid and never hits.
type tarCache struct {
	mu       sync.Mutex
	maxBytes int64
	curBytes int64
	entries  map[string]*list.Element
	order    *list.List // front = most recently used
}

// newTarCache returns a cache bounded to maxBytes, nil (disabled) for <= 0.
func newTarCache(maxBytes int64) *tarCache {
	if maxBytes <= 0 {
		return nil
	}
	return &tarCache{maxBytes: maxBytes, entries: make(map[string]*list.Element), order: list.New()}
}

func (c *tarCache) get(key string) (*tarEntry, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*tarEntry), true
}

// put stores e, evicting the least recently used past the budget. An entry
// larger than the whole budget is not kept: it would only evict everything.
func (c *tarCache) put(e *tarEntry) {
	if c == nil || e == nil || int64(len(e.data)) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[e.key]; ok {
		c.curBytes += int64(len(e.data)) - int64(len(el.Value.(*tarEntry).data))
		el.Value = e
		c.order.MoveToFront(el)
	} else {
		c.entries[e.key] = c.order.PushFront(e)
		c.curBytes += int64(len(e.data))
	}
	for c.curBytes > c.maxBytes {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		ev := oldest.Value.(*tarEntry)
		c.order.Remove(oldest)
		delete(c.entries, ev.key)
		c.curBytes -= int64(len(ev.data))
	}
}

func (c *tarCache) size() (entries int, bytes int64) {
	if c == nil {
		return 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.curBytes
}
