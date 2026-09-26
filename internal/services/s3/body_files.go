package s3

// body_files.go keeps the bytes half of the S3 store. Every object version's
// body and every multipart part is a file under one directory; the records
// describing them live in state.Store (store.go). A write only ever becomes
// visible whole: its bytes are staged first and moved into place in one
// rename once they are all there (#2192).

import (
	"crypto/md5"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// stagingDir holds bodies whose write has not completed. It sits inside the
// body directory, so moving a finished body into place is a rename within one
// filesystem, and its name starts with a dot, which no bucket name may, so it
// never shares a directory with a bucket's bodies.
const stagingDir = ".staging"

// bodyFill streams a body's bytes into w and reports how many it wrote.
type bodyFill func(w io.Writer) (int64, error)

// copyFrom is the bodyFill for a body that arrives as a reader.
func copyFrom(r io.Reader) bodyFill {
	return func(w io.Writer) (int64, error) { return io.Copy(w, r) }
}

// bodyDigest describes a body once all of it has been written.
type bodyDigest struct {
	md5  []byte
	size int64
}

// etag is the ETag S3 gives a body stored in one piece: its quoted MD5.
func (d bodyDigest) etag() string { return fmt.Sprintf(`"%x"`, d.md5) }

// bodyFiles is the directory of body files.
//
// Every access goes through an os.Root. Beyond confining paths to the
// directory, that is what makes replacing a body safe on Windows: a file
// opened through os.Root shares delete access, and os.Root's Rename and Remove
// use POSIX semantics, so a body can be replaced or removed while a GetObject
// is still streaming the old bytes. os.Open and os.Rename refuse exactly that
// there ("Access is denied").
type bodyFiles struct {
	dir string

	// prepared guards prepare, which runs on first use rather than at
	// construction — New must not touch the disk (AGENTS.md startup budget).
	prepared sync.Once

	// installs serialises installing a body at a path with committing the
	// record that describes it, so two writers of one path cannot leave one's
	// bytes under the other's record.
	installs serviceutil.RecordLocks
}

func newBodyFiles(dir string) *bodyFiles {
	return &bodyFiles{dir: dir}
}

// root opens the body directory. The caller closes it.
func (b *bodyFiles) root() (*os.Root, error) {
	b.prepared.Do(b.prepare)
	return os.OpenRoot(b.dir)
}

// prepare creates the directory and discards whatever a previous process left
// staged: a staged body belongs to a write that never completed, so no record
// refers to it. Neither step can usefully fail here — a directory that cannot
// be created fails the OpenRoot that follows, and a leftover that cannot be
// removed costs only disk space.
func (b *bodyFiles) prepare() {
	_ = os.MkdirAll(b.dir, 0o755)
	_ = os.RemoveAll(filepath.Join(b.dir, stagingDir))
}

// open opens the body at rel for reading. The caller closes it.
func (b *bodyFiles) open(rel string) (*os.File, error) {
	root, err := b.root()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(rel)
}

// remove deletes the file, or empty directory, at rel.
func (b *bodyFiles) remove(rel string) error {
	root, err := b.root()
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Remove(rel)
}

// removeAll deletes rel and everything beneath it.
func (b *bodyFiles) removeAll(rel string) error {
	root, err := b.root()
	if err != nil {
		return err
	}
	defer root.Close()
	return root.RemoveAll(rel)
}

// replace writes a new body at rel so that it either takes effect completely
// or not at all.
//
// fill streams the bytes into a staged file; nothing at rel changes while it
// runs, so a fill that fails — a client that disconnects, a producer that
// gives up — leaves whatever rel held untouched. Once fill has succeeded the
// staged file is renamed over rel, and commit is handed its digest to store
// the record that describes it. If commit fails, rel is put back as it was:
// the previous body is restored from a hard link taken before the rename, or
// the new file is removed when there was no previous body. No staged file
// outlives the call.
func (b *bodyFiles) replace(rel string, fill bodyFill, commit func(bodyDigest) *protocol.AWSError) *protocol.AWSError {
	root, err := b.root()
	if err != nil {
		return bodyError(rel, err)
	}
	defer root.Close()

	staged, digest, err := stageBody(root, fill)
	if err != nil {
		return bodyError(rel, err)
	}
	defer func() { _ = root.Remove(staged) }() // already gone once installed

	defer b.installs.Lock(rel)()
	undo, done, err := installBody(root, staged, rel)
	if err != nil {
		return bodyError(rel, err)
	}
	if aerr := commit(digest); aerr != nil {
		undo()
		return aerr
	}
	done()
	return nil
}

// stageBody writes fill's bytes to a new file in stagingDir, hashing them on
// the way, and returns the file's name. A fill that fails leaves no file.
func stageBody(root *os.Root, fill bodyFill) (string, bodyDigest, error) {
	if err := root.MkdirAll(stagingDir, 0o755); err != nil {
		return "", bodyDigest{}, err
	}
	name := filepath.Join(stagingDir, rand.Text())
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return "", bodyDigest{}, err
	}
	h := md5.New()
	n, err := fill(io.MultiWriter(f, h))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = root.Remove(name)
		return "", bodyDigest{}, err
	}
	return name, bodyDigest{md5: h.Sum(nil), size: n}, nil
}

// installBody renames staged over rel in one step. Before it does, it keeps a
// hard link to whatever rel held, so the swap can be reversed: undo puts rel
// back as it was, and done drops the kept link once the swap is final.
//
// On a filesystem without hard links the previous body cannot be kept, so undo
// leaves the new one in place rather than lose both.
func installBody(root *os.Root, staged, rel string) (undo, done func(), err error) {
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return nil, nil, err
	}
	kept := staged + ".prev"
	linkErr := root.Link(rel, kept)
	if err := root.Rename(staged, rel); err != nil {
		if linkErr == nil {
			_ = root.Remove(kept)
		}
		return nil, nil, err
	}
	switch {
	case linkErr == nil:
		undo = func() { _ = root.Rename(kept, rel) }
		done = func() { _ = root.Remove(kept) }
	case errors.Is(linkErr, fs.ErrNotExist):
		undo = func() { _ = root.Remove(rel) }
		done = func() {}
	default:
		undo, done = func() {}, func() {}
	}
	return undo, done, nil
}

// bodyError reports a body file that could not be written as S3's
// InternalError, keeping the cause for the log.
func bodyError(rel string, err error) *protocol.AWSError {
	return protocol.Wrap(protocol.ErrInternalError, fmt.Errorf("s3: write body %s: %w", filepath.ToSlash(rel), err))
}
