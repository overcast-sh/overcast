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

	// swept guards sweepStaged, which runs on first use rather than at
	// construction — New must not touch the disk (AGENTS.md startup budget).
	swept sync.Once

	// paths serialises every change to what a body path holds together with
	// the records that describe it — installing a body and committing its
	// records, or removing a body with its record — so two writers of one path
	// cannot leave one's bytes under the other's records. Every null version of
	// a key shares one path, which is why a suspended bucket's null-version
	// bookkeeping happens under it too.
	paths serviceutil.RecordLocks
}

func newBodyFiles(dir string) *bodyFiles {
	return &bodyFiles{dir: dir}
}

// root opens the body directory, creating it on first need. The caller
// closes it.
func (b *bodyFiles) root() (*os.Root, error) {
	b.swept.Do(b.sweepStaged)
	root, err := os.OpenRoot(b.dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(b.dir, 0o755); err != nil {
			return nil, err
		}
		root, err = os.OpenRoot(b.dir)
	}
	return root, err
}

// sweepStaged discards whatever a previous process left staged: a staged body
// belongs to a write that never completed, so no record refers to it. A
// leftover that cannot be removed costs only disk space, so a failure here is
// not worth failing a request over.
func (b *bodyFiles) sweepStaged() {
	_ = os.RemoveAll(filepath.Join(b.dir, stagingDir))
}

// locked runs fn holding rel's path lock, for a change to what rel holds that
// installs no new body: removing a version, or a delete marker replacing the
// null version whose body is stored there.
func (b *bodyFiles) locked(rel string, fn func() *protocol.AWSError) *protocol.AWSError {
	defer b.paths.Lock(rel)()
	return fn()
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

// copyTo copies the body at rel into w.
func (b *bodyFiles) copyTo(w io.Writer, rel string) (int64, error) {
	f, err := b.open(rel)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(w, f)
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
// the records that describe it, under rel's path lock. If commit fails, rel is
// put back as it was: the previous body is restored from where it was kept
// before the rename, or the new file is removed when there was no previous
// body. commit must therefore fail only before anything it stored is visible.
// No staged file outlives the call.
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

	defer b.paths.Lock(rel)()
	inst, err := installBody(root, staged, rel)
	if err != nil {
		return bodyError(rel, err)
	}
	if aerr := commit(digest); aerr != nil {
		if err := inst.undo(); err != nil {
			return bodyError(rel, errors.Join(aerr, fmt.Errorf("restore previous body: %w", err)))
		}
		return aerr
	}
	inst.done()
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

// installation is a body renamed into place, with what rel held before it
// kept aside until the swap is final.
type installation struct {
	root *os.Root
	rel  string
	kept string // "" when rel held nothing
}

// installBody keeps whatever rel holds, then renames staged over it.
//
// The previous body is kept as a hard link, so rel is replaced in one atomic
// rename and a concurrent reader never finds it missing. On a filesystem
// without hard links it is renamed aside instead, which leaves rel briefly
// absent but still lets a failed commit put it back.
func installBody(root *os.Root, staged, rel string) (installation, error) {
	inst := installation{root: root, rel: rel, kept: staged + ".prev"}
	switch err := root.Link(rel, inst.kept); {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		inst.kept = ""
	default:
		if err := root.Rename(rel, inst.kept); err != nil {
			return installation{}, err
		}
	}
	if err := renameInto(root, staged, rel); err != nil {
		_ = inst.undo()
		return installation{}, err
	}
	return inst, nil
}

// renameInto renames staged to rel, creating rel's directory. A deleteObject
// that removes a bucket directory it just emptied can race the directory's
// creation, so a rename that finds it gone creates it again once.
func renameInto(root *os.Root, staged, rel string) error {
	var err error
	for range 2 {
		if err = root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
			return err
		}
		if err = root.Rename(staged, rel); !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return err
}

// undo puts rel back as it was before the installation.
func (i installation) undo() error {
	if i.kept == "" {
		if err := i.root.Remove(i.rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return i.root.Rename(i.kept, i.rel)
}

// done drops the previous body once the swap is final.
func (i installation) done() {
	if i.kept != "" {
		_ = i.root.Remove(i.kept)
	}
}

// bodyError reports a body file that could not be written as S3's
// InternalError, keeping the cause for the log.
func bodyError(rel string, err error) *protocol.AWSError {
	return protocol.Wrap(protocol.ErrInternalError, fmt.Errorf("s3: write body %s: %w", filepath.ToSlash(rel), err))
}
