package s3

// version.go owns S3 object version history: the version keyspace, how a
// version id is minted, and the store helpers the version-aware object routes
// and the lifecycle sweeper share.
//
// ---- The keyspace ----------------------------------------------------------
//
// Two namespaces, with one invariant between them:
//
//	s3:objects   "<bucket>/<key>"                  the CURRENT version
//	s3:versions  "<bucket>/<key>\x01<seq>"         every version of that key
//
// s3:objects is unchanged from before version history existed: it still holds
// exactly one record per live key, which is what keeps ListObjects{,V2},
// HeadObject and GetObject a single store read and keeps a bucket that never
// enables versioning byte-for-byte identical to how it behaved before.
//
// s3:versions is written only for buckets whose versioning status is Enabled
// or Suspended — see Bucket.versioned. A bucket that has never been versioned
// has no records there at all, so it pays nothing for the feature.
//
// <seq> is versionStamp.sortToken: 24 characters of base32hex, chosen because
// that alphabet ("0123456789ABCDEFGHIJKLMNOPQRSTUV") is in ascending ASCII
// order, so the encoded string sorts exactly as the bytes it encodes do. The
// bytes are the mint time and a counter, both bitwise-inverted, so a NEWER
// version sorts BEFORE an older one. A plain lexicographic scan of s3:versions
// therefore already produces ListObjectVersions' documented order — "objects
// in alphabetical order [by key], and within each key, versions in the order
// they were stored, most recent first" — with no in-memory sort and with the
// storage key itself usable as a resumable pagination cursor.
//
// \x01 separates key from seq because it sorts below every byte a real key
// starts with, which is what makes all versions of "a" group together and sort
// before every version of "a/b". A key containing a literal 0x01 would group
// oddly; S3 permits any UTF-8 in a key, but no client emits control characters
// there, and the failure mode is misordering rather than data loss.
//
// ---- Version ids -----------------------------------------------------------
//
// AWS's version ids are opaque, URL-safe and carry no documented meaning, so
// Overcast is free to make them sortable. VersionID is the same token used in
// the storage key, so looking up a version by id is a direct Get rather than a
// scan.
//
// The one exception is the null version. AWS gives version id "null" to an
// object that was stored while the bucket was unversioned or version-suspended,
// and a key has at most one of them. Overcast models it as VersionID == "" and
// renders it as "null" on the wire; it still carries a Seq so it sorts with the
// rest of the history.
//
// ---- Objects stored before this existed ------------------------------------
//
// Every new field on Object is `omitempty`, so a record persisted by an older
// build decodes as VersionID "" / Seq "" / DeleteMarker false — which is
// exactly the null version of an unversioned key, and exactly what AWS says
// such an object is. Nothing has to be rewritten for reads to stay correct, and
// no list can 500 on one.
//
// Those records do need a Seq before they can take part in ordered version
// history. ensureVersionHistory backfills them once per bucket, driven off
// Bucket.VersionHistoryReady, deriving the Seq deterministically from the
// object's LastModified so a re-run produces the same token. It skips every
// object that already has one, so it is safe to run repeatedly and safe to
// interrupt.

import (
	"context"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	// nsVersions stores one record per object version, current and noncurrent
	// alike, for buckets that are versioning-enabled or -suspended.
	nsVersions = "s3:versions"

	// versionSep separates an object key from its sort token inside a
	// s3:versions storage key. See the keyspace note above for why it is 0x01.
	versionSep = "\x01"

	// nullVersionID is the version id AWS reports for an object stored while
	// the bucket was unversioned or version-suspended.
	nullVersionID = "null"

	// versionScanHigh sorts above every character a sort token can contain
	// (base32hex tops out at 'V'), so appending it to a key skips that key's
	// whole version group during a scan.
	versionScanHigh = "￿"
)

// versionTokenEncoding is base32hex without padding: 15 bytes in, 24
// order-preserving characters out.
var versionTokenEncoding = base32.HexEncoding.WithPadding(base32.NoPadding)

// versionCounter breaks ties between two versions minted at the same clock
// reading. That is not a theoretical case: tests drive a mock clock that does
// not advance between requests, so without it two consecutive PutObject calls
// would mint interchangeable tokens and version order would be undefined.
//
// It resets on restart, which only matters if a version were minted at the very
// same nanosecond as one from a previous process — impossible against a real
// clock, and harmless against a mock one.
var versionCounter atomic.Uint32

// versionStamp is one mint: the clock reading and counter behind both a
// version id and an event sequencer.
type versionStamp struct {
	nanos uint64
	seq   uint32
	salt  uint32
}

// newVersionStamp mints a stamp from the injected clock's reading. Never call
// time.Now here — the sweeper and every handler pass h.clk.Now().
func newVersionStamp(now time.Time) versionStamp {
	return versionStamp{
		nanos: clampNanos(now),
		seq:   versionCounter.Add(1),
		salt:  rand.Uint32(), //nolint:gosec // opacity, not secrecy: version ids are public identifiers
	}
}

// legacyVersionStamp derives a stamp for an object persisted before version
// history existed. It is a pure function of the object's LastModified, so the
// backfill produces the same token every time it runs.
func legacyVersionStamp(lastModified time.Time) versionStamp {
	return versionStamp{nanos: clampNanos(lastModified)}
}

// clampNanos maps a time to unsigned nanoseconds, flooring anything before the
// Unix epoch at zero so an unset timestamp cannot invert into a token that
// sorts newer than everything else.
func clampNanos(t time.Time) uint64 {
	nanos := t.UTC().UnixNano()
	if nanos < 0 {
		return 0
	}
	return uint64(nanos)
}

// sortToken renders the stamp as the 24-character storage-key component and
// version id. Time and counter are inverted so newest sorts first.
func (s versionStamp) sortToken() string {
	var b [15]byte
	binary.BigEndian.PutUint64(b[0:8], ^s.nanos)
	binary.BigEndian.PutUint32(b[8:12], ^s.seq)
	// Three bytes of salt keep the id from reading as a plain timestamp. They
	// are the least significant part of the token, so they never disturb the
	// recency order the first twelve bytes establish.
	b[12], b[13], b[14] = byte(s.salt>>16), byte(s.salt>>8), byte(s.salt)
	return versionTokenEncoding.EncodeToString(b[:])
}

// sequencer renders the stamp as S3's event-notification sequencer: an
// uppercase hex string that increases with each event for a key, which is what
// AWS documents consumers to compare in order to sequence events.
func (s versionStamp) sequencer() string {
	var b [12]byte
	binary.BigEndian.PutUint64(b[0:8], s.nanos)
	binary.BigEndian.PutUint32(b[8:12], s.seq)
	return strings.ToUpper(hex.EncodeToString(b[:]))
}

// versioned reports whether the bucket keeps version history. Suspended counts:
// it is not "off" — a suspended bucket still retains the versions it already
// has, still creates null-version delete markers, and can be re-enabled.
func (b *Bucket) versioned() bool {
	return b.VersioningStatus == versioningEnabled || b.VersioningStatus == versioningSuspended
}

const (
	versioningEnabled   = "Enabled"
	versioningSuspended = "Suspended"
)

// wireVersionID returns the VersionId S3 reports for this version.
func (o *Object) wireVersionID() string {
	if o.VersionID == "" {
		return nullVersionID
	}
	return o.VersionID
}

// isNullVersion reports whether this is the key's null version.
func (o *Object) isNullVersion() bool { return o.VersionID == "" }

// ---- Storage keys ----------------------------------------------------------

// versionStoreKey builds the s3:versions key for one version.
func versionStoreKey(bucket, key, seq string) string {
	return bucket + "/" + key + versionSep + seq
}

// versionScanPrefix is the s3:versions prefix covering every version of every
// key in bucket that starts with prefix.
func versionScanPrefix(bucket, prefix string) string {
	return bucket + "/" + prefix
}

// splitVersionSuffix splits a s3:versions storage key with its "<bucket>/"
// prefix already removed into the object key and the sort token.
func splitVersionSuffix(suffix string) (key, seq string, ok bool) {
	idx := strings.LastIndex(suffix, versionSep)
	if idx < 0 {
		return "", "", false
	}
	return suffix[:idx], suffix[idx+1:], true
}

// ---- Store access ----------------------------------------------------------

// putVersion persists one version record. The body, when there is one, is
// written by storeObject under the version's own body path.
func (s *s3Store) putVersion(ctx context.Context, obj *Object) *protocol.AWSError {
	raw, err := json.Marshal(obj)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsVersions, versionStoreKey(obj.Bucket, obj.Key, obj.Seq), string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// deleteVersion removes one version record and its body file.
func (s *s3Store) deleteVersion(ctx context.Context, obj *Object) *protocol.AWSError {
	if aerr := s.deleteVersionRecord(ctx, obj); aerr != nil {
		return aerr
	}
	if obj.DeleteMarker {
		return nil // a delete marker has no body
	}
	if err := s.bodies.remove(bodyRel(obj.Bucket, obj.Key, obj.VersionID)); err != nil && !os.IsNotExist(err) {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// deleteVersionRecord removes one version record and leaves its body path
// alone, for a version whose body a newer one has already replaced on disk.
func (s *s3Store) deleteVersionRecord(ctx context.Context, obj *Object) *protocol.AWSError {
	if err := s.store.Delete(ctx, nsVersions, versionStoreKey(obj.Bucket, obj.Key, obj.Seq)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// listKeyVersions returns every stored version of one key, newest first.
// A malformed record is skipped rather than failing the key
// (CONTRIBUTING.md malformed-persisted-state policy).
func (s *s3Store) listKeyVersions(ctx context.Context, bucket, key string) ([]*Object, *protocol.AWSError) {
	pairs, err := s.store.Scan(ctx, nsVersions, bucket+"/"+key+versionSep)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return decodeVersionPairs(pairs), nil
}

// findVersion resolves a wire version id to its stored record. A real version
// id is its own sort token, so that is a direct Get; "null" needs the key's
// version list because the null version's token is minted, not derived from
// its id.
func (s *s3Store) findVersion(ctx context.Context, bucket, key, versionID string) (*Object, bool, *protocol.AWSError) {
	if versionID == nullVersionID {
		versions, aerr := s.listKeyVersions(ctx, bucket, key)
		if aerr != nil {
			return nil, false, aerr
		}
		for _, v := range versions {
			if v.isNullVersion() {
				return v, true, nil
			}
		}
		return nil, false, nil
	}

	raw, found, err := s.store.Get(ctx, nsVersions, versionStoreKey(bucket, key, versionID))
	if err != nil {
		return nil, false, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, false, nil
	}
	var obj Object
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		// A single unreadable record reads as "no such version" rather than a
		// 500 for the whole bucket.
		return nil, false, nil
	}
	return &obj, true, nil
}

// listVersionsPage returns up to limit version records for bucket whose keys
// start with prefix, in AWS's list order, starting strictly after the storage
// suffix startAfter ("<key>\x01<seq>", i.e. the storage key minus "<bucket>/").
// The returned cursor is the suffix to pass back for the next page, or "" at
// the end of the range.
func (s *s3Store) listVersionsPage(ctx context.Context, bucket, prefix, startAfter string, limit int) ([]*Object, string, *protocol.AWSError) {
	storageStartAfter := ""
	if startAfter != "" {
		storageStartAfter = bucket + "/" + startAfter
	}
	pairs, nextStorageKey, err := s.store.ScanPage(ctx, nsVersions, versionScanPrefix(bucket, prefix), storageStartAfter, limit)
	if err != nil {
		return nil, "", protocol.Wrap(protocol.ErrInternalError, err)
	}
	next := ""
	if nextStorageKey != "" {
		next = nextStorageKey[len(bucket)+1:]
	}
	return decodeVersionPairs(pairs), next, nil
}

// decodeVersionPairs decodes scanned records, dropping any that will not parse.
// The scan cursor tracks raw storage keys, so skipping a record here never
// skips the position it occupied.
func decodeVersionPairs(pairs []state.KV) []*Object {
	out := make([]*Object, 0, len(pairs))
	for _, kv := range pairs {
		var obj Object
		if err := json.Unmarshal([]byte(kv.Value), &obj); err != nil {
			// One malformed persisted record must not fail the whole list
			// (CONTRIBUTING.md malformed-persisted-state rule).
			continue
		}
		if obj.Seq == "" {
			// A record whose sort token did not survive cannot be addressed,
			// ordered or paginated. Recover what the storage key still says
			// rather than emitting an entry the caller cannot act on.
			key, seq, ok := splitVersionSuffix(strings.TrimPrefix(kv.Key, obj.Bucket+"/"))
			if !ok {
				continue
			}
			obj.Seq = seq
			if obj.Key == "" {
				obj.Key = key
			}
		}
		out = append(out, &obj)
	}
	return out
}

// ---- Backfill --------------------------------------------------------------

// ensureVersionHistory gives every object stored before this bucket had version
// history a place in it, so ListObjectVersions and the sweeper see one complete,
// ordered history rather than a mix of versioned and unversioned records.
//
// It is a no-op for a bucket that has never been versioned, and a single
// already-loaded boolean for one that has been backfilled. Otherwise it pages
// s3:objects, mints each legacy object a deterministic sort token derived from
// its LastModified, writes it into s3:versions as the key's null version, and
// records the token on the s3:objects record so the two agree.
//
// Re-run safety: objects that already carry a token are skipped, the derived
// token is a pure function of data the migration does not change, and the
// completion flag is only written after a clean pass. An interrupted run leaves
// a partially migrated bucket that the next run finishes.
//
// The ambiguous case, stated plainly: a legacy object's LastModified is the
// only ordering information that survives, and it has second-or-better
// resolution rather than the nanosecond resolution a minted token has. Two
// legacy objects under the SAME key cannot exist — s3:objects holds one record
// per key — so the derived tokens only ever have to order one version, and
// ambiguity between two legacy objects can never arise. What can happen is a
// legacy null version and a later minted version sharing a nanosecond; the
// minted one carries a nonzero counter, whose inversion sorts it ahead, which
// is the correct answer because it was stored later.
func (h *Handler) ensureVersionHistory(ctx context.Context, b *Bucket) *protocol.AWSError {
	if b == nil || !b.versioned() || b.VersionHistoryReady {
		return nil
	}

	startAfter := ""
	for {
		objects, next, aerr := h.store.listObjectsPage(ctx, b.Name, "", startAfter, lifecycleSweepPageSize)
		if aerr != nil {
			return aerr
		}
		for _, obj := range objects {
			if obj.Seq != "" {
				continue
			}
			obj.Seq = legacyVersionStamp(obj.LastModified).sortToken()
			if aerr := h.store.putVersion(ctx, obj); aerr != nil {
				return aerr
			}
			if aerr := h.store.putObjectMeta(ctx, obj); aerr != nil {
				return aerr
			}
		}
		if next == "" {
			break
		}
		startAfter = next
	}

	b.VersionHistoryReady = true
	return h.store.putBucket(ctx, b)
}

// recordExistingVersion makes sure the key's current object is present in
// s3:versions before a new version displaces it. It is the per-key half of the
// backfill above: PutObject and DeleteObject reach a key one at a time and must
// not lose the version they are superseding, even if the bucket-wide backfill
// has not run yet.
func (h *Handler) recordExistingVersion(ctx context.Context, obj *Object) *protocol.AWSError {
	if obj == nil {
		return nil
	}
	if obj.Seq == "" {
		obj.Seq = legacyVersionStamp(obj.LastModified).sortToken()
	}
	return h.store.putVersion(ctx, obj)
}

// beginVersion gives obj the identity of the key's next version and preserves
// whatever that key currently holds, returning the stamp minted for it.
//
// For a bucket that has never been versioned this only mints the stamp — the
// object gets no version id and no history record, and the write that follows
// is the same single-record write it has always been. The stamp is still needed
// there because S3 puts a sequencer on every object event, versioned or not.
func (h *Handler) beginVersion(ctx context.Context, b *Bucket, obj *Object, now time.Time) (versionStamp, *protocol.AWSError) {
	stamp := newVersionStamp(now)
	if !b.versioned() {
		return stamp, nil
	}

	obj.Seq = stamp.sortToken()
	if b.VersioningStatus == versioningEnabled {
		// A suspended bucket stores its writes as the null version, which is
		// modelled by leaving VersionID empty.
		obj.VersionID = obj.Seq
	}

	// Whatever is current is about to become noncurrent, so it has to be in the
	// history before it is displaced — including an object written before this
	// bucket had any history at all.
	prev, aerr := h.store.lookupObjectMeta(ctx, obj.Bucket, obj.Key)
	if aerr != nil {
		return stamp, aerr
	}
	if aerr := h.recordExistingVersion(ctx, prev); aerr != nil {
		return stamp, aerr
	}
	return stamp, nil
}

// commitVersion records obj in the key's history once its body and current-
// version record are in place, and in a version-suspended bucket drops the
// null version obj replaced. A no-op for an unversioned bucket.
//
// The null version goes only now, not in beginVersion: until obj is committed
// the write can still fail, and a failed write must leave the key's history
// exactly as it was (#2192).
func (h *Handler) commitVersion(ctx context.Context, b *Bucket, obj *Object) *protocol.AWSError {
	if !b.versioned() {
		return nil
	}
	if aerr := h.store.putVersion(ctx, obj); aerr != nil {
		return aerr
	}
	if b.VersioningStatus == versioningSuspended {
		return h.replaceNullVersion(ctx, obj)
	}
	return nil
}

// saveCurrentObject persists a change to the key's current version and keeps
// its history record in step.
//
// Metadata that can change after the write — object tags above all, because a
// lifecycle rule filters on them — would otherwise live only on the
// current-version pointer, where the version-aware sweeper never looks.
func (h *Handler) saveCurrentObject(ctx context.Context, obj *Object) *protocol.AWSError {
	if aerr := h.store.putObjectMeta(ctx, obj); aerr != nil {
		return aerr
	}
	if obj.Seq == "" {
		return nil
	}
	return h.store.putVersion(ctx, obj)
}

// setVersionIDHeader writes x-amz-version-id, which S3 sends only for an object
// that lives in a versioned bucket. Seq is what distinguishes those: every
// version Overcast records carries one, and an object in a bucket that has
// never been versioned never does.
func setVersionIDHeader(w http.ResponseWriter, obj *Object) {
	if v := obj.headerVersionID(); v != "" {
		w.Header().Set("x-amz-version-id", v)
	}
}

// headerVersionID is the x-amz-version-id value S3 reports for obj, or "" when
// it sends none — see setVersionIDHeader.
func (o *Object) headerVersionID() string {
	if o.Seq == "" {
		return ""
	}
	return o.wireVersionID()
}

// replaceNullVersion removes the null version that obj, a new null version
// already committed, has replaced.
//
// A version-suspended bucket keeps at most one null version per key: AWS
// documents that storing an object in a suspended bucket "removes the existing
// object with a version ID of null" and replaces it. Every other version is
// left alone.
//
// Every null version keeps its body at the same path, so a new object's body
// has already taken the old one's place on disk and only the old record goes.
// A delete marker has no body, so the old body goes with its record.
func (h *Handler) replaceNullVersion(ctx context.Context, obj *Object) *protocol.AWSError {
	versions, aerr := h.store.listKeyVersions(ctx, obj.Bucket, obj.Key)
	if aerr != nil {
		return aerr
	}
	drop := h.store.deleteVersionRecord
	if obj.DeleteMarker {
		drop = h.store.deleteVersion
	}
	for _, v := range versions {
		if !v.isNullVersion() || v.Seq == obj.Seq {
			continue
		}
		if aerr := drop(ctx, v); aerr != nil {
			return aerr
		}
	}
	return nil
}

// promoteNewestVersion repoints the key's current record at the newest
// remaining version after a version-targeted delete removed the current one,
// and clears the record entirely when nothing is left.
func (h *Handler) promoteNewestVersion(ctx context.Context, bucket, key string) *protocol.AWSError {
	versions, aerr := h.store.listKeyVersions(ctx, bucket, key)
	if aerr != nil {
		return aerr
	}
	if len(versions) == 0 {
		return h.store.deleteObjectRecord(ctx, bucket, key)
	}
	return h.store.putObjectMeta(ctx, versions[0])
}

// ---- Version-specific errors -----------------------------------------------

func errNoSuchVersion() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchVersion",
		Message:    "The specified version does not exist.",
		HTTPStatus: http.StatusNotFound,
	}
}

// errMethodNotAllowedOnDeleteMarker is what S3 answers a GET or HEAD that names
// a delete marker's version id: the version exists, but reading it is not a
// thing you can do.
func errMethodNotAllowedOnDeleteMarker() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "MethodNotAllowed",
		Message:    "The specified method is not allowed against this resource.",
		HTTPStatus: http.StatusMethodNotAllowed,
	}
}

func errInvalidVersionID() *protocol.AWSError {
	return protocol.ErrInvalidArgument("Invalid version id specified")
}
