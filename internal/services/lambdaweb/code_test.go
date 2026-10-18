package lambdaweb

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/services/s3"
)

// fakeS3 is the slice of the S3 service the code source reads.
type fakeS3 struct {
	versioning map[string]string // bucket -> status; absent bucket is NoSuchBucket
	heads      map[string]s3.ObjectHead
	bodies     map[string][]byte
}

func s3NotFound(code string) *protocol.AWSError {
	return &protocol.AWSError{Code: code, Message: code, HTTPStatus: http.StatusNotFound}
}

func (f *fakeS3) BucketVersioning(_ context.Context, bucket string) (string, *protocol.AWSError) {
	st, ok := f.versioning[bucket]
	if !ok {
		return "", s3NotFound("NoSuchBucket")
	}
	return st, nil
}

func (f *fakeS3) HeadObjectVersion(_ context.Context, bucket, key, versionID string) (s3.ObjectHead, *protocol.AWSError) {
	h, ok := f.heads[bucket+"/"+key+"@"+versionID]
	if !ok {
		if versionID != "" {
			return s3.ObjectHead{}, s3NotFound("NoSuchVersion")
		}
		return s3.ObjectHead{}, s3NotFound("NoSuchKey")
	}
	return h, nil
}

func (f *fakeS3) GetObjectBytes(_ context.Context, bucket, key, versionID string) ([]byte, *protocol.AWSError) {
	b, ok := f.bodies[bucket+"/"+key+"@"+versionID]
	if !ok {
		return nil, s3NotFound("NoSuchKey")
	}
	return b, nil
}

func TestS3CodeSource_resolve(t *testing.T) {
	fake := &fakeS3{
		versioning: map[string]string{"code": "Enabled", "plain": "", "paused": "Suspended"},
		heads: map[string]s3.ObjectHead{
			"code/app.zip@":      {VersionID: "ver-2", Size: 1234},
			"code/app.zip@ver-1": {VersionID: "ver-1", Size: 99},
			"code/gone.zip@":     {VersionID: "ver-9", DeleteMarker: true},
			"code/huge.zip@":     {VersionID: "ver-3", Size: maxPackageBytes + 1},
			"plain/app.zip@":     {Size: 1},
			"paused/app.zip@":    {Size: 1},
		},
	}
	cases := []struct {
		name     string
		ref      codeRef
		wantVer  string
		wantSize int64
		wantErr  string // substring of a ValidationException message
	}{
		{name: "latest version is pinned", ref: codeRef{Bucket: "code", Key: "app.zip"}, wantVer: "ver-2", wantSize: 1234},
		{name: "explicit version is kept", ref: codeRef{Bucket: "code", Key: "app.zip", VersionID: "ver-1"}, wantVer: "ver-1", wantSize: 99},
		{name: "missing bucket", ref: codeRef{Bucket: "nope", Key: "app.zip"}, wantErr: "does not exist"},
		{name: "unversioned bucket", ref: codeRef{Bucket: "plain", Key: "app.zip"}, wantErr: "versioning enabled"},
		{name: "suspended bucket", ref: codeRef{Bucket: "paused", Key: "app.zip"}, wantErr: "versioning enabled"},
		{name: "missing key", ref: codeRef{Bucket: "code", Key: "nope.zip"}, wantErr: "does not exist"},
		{name: "missing version", ref: codeRef{Bucket: "code", Key: "app.zip", VersionID: "ver-7"}, wantErr: "ver-7"},
		{name: "current version is a delete marker", ref: codeRef{Bucket: "code", Key: "gone.zip"}, wantErr: "does not exist"},
		{name: "over 512 MB", ref: codeRef{Bucket: "code", Key: "huge.zip"}, wantErr: "512 MB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a code source over S3
			src := NewS3CodeSource(fake)

			// When: a reference is resolved
			pinned, size, aerr := src.Resolve(context.Background(), tc.ref)

			// Then: it is pinned, or refused with a ValidationException
			if tc.wantErr != "" {
				if aerr == nil || aerr.Code != "ValidationException" || !strings.Contains(aerr.Message, tc.wantErr) {
					t.Fatalf("error = %+v, want ValidationException containing %q", aerr, tc.wantErr)
				}
				return
			}
			if aerr != nil || pinned.VersionID != tc.wantVer || size != tc.wantSize {
				t.Errorf("Resolve = %+v, %d, %v; want version %s size %d", pinned, size, aerr, tc.wantVer, tc.wantSize)
			}
		})
	}
}

func TestS3CodeSource_fetchReadsThePinnedVersion(t *testing.T) {
	// Given: two versions of an object
	fake := &fakeS3{bodies: map[string][]byte{"code/app.zip@ver-1": []byte("old"), "code/app.zip@ver-2": []byte("new")}}
	src := NewS3CodeSource(fake)

	// When: the pinned older version is fetched
	body, aerr := src.Fetch(context.Background(), codeRef{Bucket: "code", Key: "app.zip", VersionID: "ver-1"})

	// Then: that version's bytes come back
	if aerr != nil || string(body) != "old" {
		t.Errorf("Fetch = %q, %v", body, aerr)
	}
}

func TestValidatePackage(t *testing.T) {
	cases := []struct {
		name     string
		pkg      func(t *testing.T) []byte
		wantCode string
	}{
		{"valid", func(t *testing.T) []byte { return makeZip(t, map[string]string{"index.js": "x", "lib/a.js": "y"}) }, ""},
		{"empty zip is a valid zip", func(t *testing.T) []byte { return makeZip(t, nil) }, ""},
		{"not a zip", func(t *testing.T) []byte { return []byte("plain text") }, codeInvalidPackage},
		{"truncated", func(t *testing.T) []byte {
			z := makeZip(t, map[string]string{"index.js": strings.Repeat("x", 1000)})
			return z[:len(z)/2]
		}, codeInvalidPackage},
		{"parent traversal", func(t *testing.T) []byte { return makeZip(t, map[string]string{"../evil.js": "x"}) }, codeUnsafePath},
		{"nested traversal", func(t *testing.T) []byte { return makeZip(t, map[string]string{"a/../../evil.js": "x"}) }, codeUnsafePath},
		{"absolute path", func(t *testing.T) []byte { return makeZip(t, map[string]string{"/etc/passwd": "x"}) }, codeUnsafePath},
		{"backslash traversal", func(t *testing.T) []byte { return makeZip(t, map[string]string{`..\evil.js`: "x"}) }, codeUnsafePath},
		{"drive letter", func(t *testing.T) []byte { return makeZip(t, map[string]string{`C:/evil.js`: "x"}) }, codeUnsafePath},
		{"corrupt member", func(t *testing.T) []byte { return corruptFirstMember(t) }, codeInvalidPackage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a package
			pkg := tc.pkg(t)

			// When: it is validated
			perr := validatePackage(pkg)

			// Then: it passes, or fails with the expected code
			switch {
			case tc.wantCode == "" && perr != nil:
				t.Errorf("unexpected failure: %v", perr)
			case tc.wantCode != "" && (perr == nil || perr.Code != tc.wantCode):
				t.Errorf("error = %v, want code %s", perr, tc.wantCode)
			}
		})
	}
}

// corruptFirstMember returns a zip whose stored member has a flipped byte, so
// the archive opens but the member fails its CRC check.
func corruptFirstMember(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "index.js", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("console.log('hello world')")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	i := bytes.Index(b, []byte("hello"))
	if i < 0 {
		t.Fatal("stored member not found")
	}
	b[i] ^= 0xff
	return b
}

func TestValidatePackage_over512MBUnzipped(t *testing.T) {
	if testing.Short() {
		t.Skip("compresses 513 MB of zeros")
	}
	// Given: a small zip that unzips to just over the limit
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(w, zeroReader{}, maxPackageBytes+1); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	// When: it is validated
	perr := validatePackage(buf.Bytes())

	// Then: it is refused as too large
	if perr == nil || perr.Code != codePackageTooLarge {
		t.Errorf("error = %v, want %s", perr, codePackageTooLarge)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestValidateRevision_rejectsReservedEnvironmentVariables(t *testing.T) {
	for _, name := range reservedEnvVars {
		// Given: a revision that sets a reserved name
		in := testRevisionConfig("code", "app.zip")
		in.Service.EnvironmentVariables = map[string]string{"OK": "1", name: "x"}

		// When: it is validated
		aerr := validateRevision(in.BuildConfig, in.Service, "", "")

		// Then: it is refused, naming the variable
		if aerr == nil || aerr.Code != "ValidationException" || !strings.Contains(aerr.Message, name) {
			t.Errorf("%s: error = %+v", name, aerr)
		}
	}

	// And: ordinary names, including ones that merely start with AWS_, are fine
	in := testRevisionConfig("code", "app.zip")
	in.Service.EnvironmentVariables = map[string]string{"AWS_LAMBDA_NODEJS_ENTRYPOINT": "dist/index.js", "AWS_PROFILE": "x", "NODE_OPTIONS": "--x"}
	if aerr := validateRevision(in.BuildConfig, in.Service, "", ""); aerr != nil {
		t.Errorf("ordinary names refused: %+v", aerr)
	}
}

func TestValidatePackage_tooManyEntries(t *testing.T) {
	// Given: a zip of empty files, one past the entry limit the provider builds
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := range providerMaxPackageEntries + 1 {
		if _, err := zw.Create(fmt.Sprintf("f%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	// When / Then: the revision's check refuses it, as the provider would
	perr := validatePackage(buf.Bytes())
	if perr == nil || perr.Code != codePackageTooLarge {
		t.Errorf("validatePackage = %+v, want %s", perr, codePackageTooLarge)
	}
}
