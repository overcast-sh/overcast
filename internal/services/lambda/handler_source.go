package lambda

// handler_source.go — emulator-only source code storage for the web UI editor.
//
// These endpoints are NOT part of the AWS Lambda API. They exist solely to
// support the built-in code editor in the Overcast web UI.
//
//   GET /_overcast/lambda/functions/{name}/source
//       Returns {"source": "...", "filename": "...", "language": "...",
//       "files": [{"name", "size"}, ...]}: the handler's file, and the list of
//       every file the function runs from.
//
//   GET /_overcast/lambda/functions/{name}/source?file=<path>
//       Returns the one file at <path> — any file in the deployment, by its
//       path under /var/task: "dist/index.js", "dist/index.js.map",
//       "node_modules/x/index.js". The path is what a Node.js inspector's
//       scriptParsed reports after "file:///var/task/", which is how the
//       console's debugger fetches source maps and the files they name
//       (docs/plans/compute-debugger-console.md § 3.3). "language" is the
//       Monaco language for the file's extension. 404 when there is no such
//       file, 400 when the function has no deployment at all.
//
//       The deployment is the function's zip, or — for a function tagged
//       overcast:hot-reload-path under OVERCAST_LAMBDA_HOT_RELOAD — the
//       mounted host directory, read live, so the console shows what the
//       container runs. The directory listing skips the dependency and VCS
//       trees the hot-reload fingerprint skips (node_modules, .git, …) and is
//       bounded like it; a file under a skipped tree is still readable by
//       path. A path is resolved inside the mount only.
//
//   PUT /_overcast/lambda/functions/{name}/source
//       Body: {"source": "...", "filename": "..."}
//       Stores the source text, packages it into an in-memory zip, updates
//       CodeZip/CodeSize, generates a new RevisionId. It edits the package,
//       never a hot-reload mount: the mount is the user's editor's.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// sourceRequest is the body for PUT .../source.
type sourceRequest struct {
	Source   string `json:"source"`
	Filename string `json:"filename"`
}

// sourceResponse is the body for GET .../source.
type sourceResponse struct {
	Source   string       `json:"source"`
	Filename string       `json:"filename"`
	Language string       `json:"language"`
	Files    []sourceFile `json:"files"` // all files in the deployment zip
	// Placeholder marks Source as an illustrative example rather than the
	// function's own code, so the console can say so instead of passing a stub
	// off as the real thing.
	Placeholder bool `json:"placeholder,omitempty"`
}

// sourceFile describes a single file inside the deployment zip.
type sourceFile struct {
	Name string `json:"name"` // path inside the zip (e.g. "src/index.js")
	Size int64  `json:"size"` // uncompressed size in bytes
}

// defaultSourceForRuntime returns a minimal handler stub for a given runtime.
func defaultSourceForRuntime(runtime, handler string) (source, filename string) {
	switch {
	case strings.HasPrefix(runtime, "nodejs"):
		filename = handlerFilename(handler, ".js")
		source = `exports.handler = async (event, context) => {
  console.log("Event:", JSON.stringify(event, null, 2));
  return {
    statusCode: 200,
    body: JSON.stringify({ message: "Hello from Lambda!" }),
  };
};
`
	case strings.HasPrefix(runtime, "python"):
		filename = handlerFilename(handler, ".py")
		source = `import json

def handler(event, context):
    print("Event:", json.dumps(event, indent=2))
    return {
        "statusCode": 200,
        "body": json.dumps({"message": "Hello from Lambda!"}),
    }
`
	case strings.HasPrefix(runtime, "java"):
		filename = "Handler.java"
		source = `import com.amazonaws.services.lambda.runtime.Context;
import com.amazonaws.services.lambda.runtime.RequestHandler;

public class Handler implements RequestHandler<Object, String> {
    @Override
    public String handleRequest(Object event, Context context) {
        return "Hello from Lambda!";
    }
}
`
	case strings.HasPrefix(runtime, "dotnet"):
		filename = "Function.cs"
		source = `using Amazon.Lambda.Core;

[assembly: LambdaSerializer(typeof(Amazon.Lambda.Serialization.SystemTextJson.DefaultLambdaJsonSerializer))]

public class Function {
    public string FunctionHandler(object input, ILambdaContext context) {
        return "Hello from Lambda!";
    }
}
`
	default:
		filename = "handler.sh"
		source = `#!/bin/bash
echo '{"statusCode": 200, "body": "Hello from Lambda!"}'
`
	}
	return source, filename
}

// handlerFilename derives a filename from the Lambda handler string.
// E.g. "index.handler" → "index.js" (for Node) or "index.py" (for Python).
func handlerFilename(handler, ext string) string {
	parts := strings.SplitN(handler, ".", 2)
	if len(parts) > 0 && parts[0] != "" {
		return parts[0] + ext
	}
	return "index" + ext
}

// runtimeLanguage maps a Lambda runtime to a Monaco editor language ID.
func runtimeLanguage(runtime string) string {
	switch {
	case strings.HasPrefix(runtime, "nodejs"):
		return "javascript"
	case strings.HasPrefix(runtime, "python"):
		return "python"
	case strings.HasPrefix(runtime, "java"):
		return "java"
	case strings.HasPrefix(runtime, "dotnet"):
		return "csharp"
	default:
		return "shell"
	}
}

// languageForFilename returns a Monaco editor language ID based on file extension,
// falling back to the runtime-based language when the extension is unrecognised.
func languageForFilename(filename, runtime string) string {
	switch {
	case strings.HasSuffix(filename, ".js"), strings.HasSuffix(filename, ".mjs"), strings.HasSuffix(filename, ".cjs"):
		return "javascript"
	case strings.HasSuffix(filename, ".jsx"):
		return "javascript"
	case strings.HasSuffix(filename, ".ts"), strings.HasSuffix(filename, ".mts"), strings.HasSuffix(filename, ".cts"):
		return "typescript"
	case strings.HasSuffix(filename, ".tsx"):
		return "typescript"
	case strings.HasSuffix(filename, ".py"):
		return "python"
	case strings.HasSuffix(filename, ".java"):
		return "java"
	case strings.HasSuffix(filename, ".cs"):
		return "csharp"
	case strings.HasSuffix(filename, ".json"), strings.HasSuffix(filename, ".map"):
		// A source map is JSON; the extension is a convention.
		return "json"
	case strings.HasSuffix(filename, ".yaml"), strings.HasSuffix(filename, ".yml"):
		return "yaml"
	case strings.HasSuffix(filename, ".md"):
		return "markdown"
	case strings.HasSuffix(filename, ".sh"), strings.HasSuffix(filename, ".bash"):
		return "shell"
	case strings.HasSuffix(filename, ".xml"):
		return "xml"
	case strings.HasSuffix(filename, ".html"):
		return "html"
	case strings.HasSuffix(filename, ".css"):
		return "css"
	default:
		return runtimeLanguage(runtime)
	}
}

// packSourceAsZip wraps source text into a minimal zip archive in memory.
func packSourceAsZip(filename, source string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(filename)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(source)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// patchZipFile replaces (or adds) a single file inside an existing zip archive.
// All other files are preserved as-is.
func patchZipFile(existingZip []byte, filename, content string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(existingZip), int64(len(existingZip)))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// Copy all existing files except the one being replaced.
	for _, f := range zr.File {
		if f.Name == filename {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		fw, err := zw.Create(f.Name)
		if err != nil {
			rc.Close()
			return nil, err
		}
		if _, err := io.Copy(fw, rc); err != nil {
			rc.Close()
			return nil, err
		}
		rc.Close()
	}

	// Write the new/updated file.
	fw, err := zw.Create(filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// resolveSource picks the source text to show for a function, and reports
// whether that text is an illustrative placeholder rather than the function's
// own code.
//
// The console used to fall back to a stub silently, so a function whose stored
// package could not be read still rendered a tidy "Hello from Lambda!". That
// made a broken deployment look healthy and hid the real fault — the displayed
// code was not the code that would run. Real code is always preferred, and when
// there is none the caller is told, so the UI can label what it is showing.
func resolveSource(fn *Function, files []sourceFile) (source, filename string, placeholder bool) {
	if fn.SourceCode != "" {
		return fn.SourceCode, fn.SourceFilename, false
	}

	if len(fn.CodeZip) > 0 {
		// Normal case: a real deployment package.
		if entry := guessEntryFile(files, fn.Handler, fn.Runtime); entry != "" {
			if content, ok := readZipFile(fn.CodeZip, entry); ok {
				return content, entry, false
			}
		}
		// Not a readable archive. Functions deployed by CloudFormation before
		// inline code was packaged correctly hold their source verbatim here,
		// so show it — it is the truth about what was deployed, and it is what
		// the user is trying to look at.
		if text, ok := asSourceText(fn.CodeZip); ok {
			return text, handlerFilename(fn.Handler, sourceExtension(fn.Runtime)), false
		}
	}

	source, filename = defaultSourceForRuntime(fn.Runtime, fn.Handler)
	return source, filename, true
}

// explainUnreadablePackage turns an opaque zip-parsing failure into something a
// user can act on when the stored package is plainly source text rather than an
// archive.
//
// Overcast versions before the inline-code fix stored a CloudFormation
// template's `Code.ZipFile` source verbatim, so functions deployed by them fail
// here with nothing but "zip: not a valid zip file" — a dead end that says
// nothing about the cause or the cure. State persists across upgrades, so
// installing the fix does not repair a function that was already deployed.
func explainUnreadablePackage(fn *Function, err error) error {
	text, ok := asSourceText(fn.CodeZip)
	if !ok || strings.TrimSpace(text) == "" {
		return err
	}
	return fmt.Errorf(
		"%w — the stored package for %q is source text, not an archive, "+
			"which means it was deployed by a version of Overcast that did not package "+
			"CloudFormation inline code (Code.ZipFile). Redeploy the stack to repair it",
		err, fn.Name,
	)
}

// asSourceText reports whether a deployment payload is plain text that can be
// shown in the editor. Binary rubble is not worth rendering; an unreadable
// package is better reported as such.
func asSourceText(code []byte) (string, bool) {
	if !utf8.Valid(code) {
		return "", false
	}
	if bytes.IndexByte(code, 0) >= 0 {
		return "", false
	}
	return string(code), true
}

// sourceExtension is the source-file extension for a runtime, used when naming
// code that was never packaged and so has no filename of its own.
func sourceExtension(runtime string) string {
	switch {
	case strings.HasPrefix(runtime, "nodejs"):
		return ".js"
	case strings.HasPrefix(runtime, "python"):
		return ".py"
	case strings.HasPrefix(runtime, "ruby"):
		return ".rb"
	default:
		return ""
	}
}

// listZipFiles returns metadata for every file in a zip archive.
func listZipFiles(zipBytes []byte) []sourceFile {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil
	}
	files := make([]sourceFile, 0, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		files = append(files, sourceFile{
			Name: f.Name,
			Size: int64(f.UncompressedSize64),
		})
	}
	return files
}

// readZipFile extracts a single file's content from a zip archive.
func readZipFile(zipBytes []byte, name string) (string, bool) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return "", false
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return "", false
			}
			defer rc.Close()
			var buf bytes.Buffer
			if _, err := buf.ReadFrom(rc); err != nil {
				return "", false
			}
			return buf.String(), true
		}
	}
	return "", false
}

// guessEntryFile picks the best file to show by default based on the handler
// string and runtime. E.g. handler "index.handler" with runtime "nodejs22.x"
// tries "index.js", "index.mjs", "index.cjs" then falls back to the first
// text-looking file.
func guessEntryFile(files []sourceFile, handler, runtime string) string {
	if len(files) == 0 {
		return ""
	}
	module := strings.SplitN(handler, ".", 2)[0]
	var exts []string
	switch {
	case strings.HasPrefix(runtime, "nodejs"):
		exts = []string{".js", ".mjs", ".cjs", ".ts"}
	case strings.HasPrefix(runtime, "python"):
		exts = []string{".py"}
	case strings.HasPrefix(runtime, "java"):
		exts = []string{".java"}
	case strings.HasPrefix(runtime, "dotnet"):
		exts = []string{".cs"}
	default:
		exts = []string{".sh"}
	}
	// Try module + ext (e.g. "index.js").
	for _, ext := range exts {
		candidate := module + ext
		for _, f := range files {
			if f.Name == candidate {
				return f.Name
			}
		}
	}
	// Fallback: first file with a known extension.
	for _, f := range files {
		for _, ext := range exts {
			if strings.HasSuffix(f.Name, ext) {
				return f.Name
			}
		}
	}
	// Last resort: first file.
	return files[0].Name
}

// sourceTree is where a function's files are read from: its deployment zip,
// or the host directory a hot-reload tag mounts at /var/task. Names are
// slash-separated paths under that root, as the container sees them.
type sourceTree interface {
	list() []sourceFile
	read(name string) (string, bool)
}

// zipTree is the deployment package.
type zipTree []byte

func (z zipTree) list() []sourceFile              { return listZipFiles(z) }
func (z zipTree) read(name string) (string, bool) { return readZipFile(z, name) }

// dirTree is a hot-reload mount, read live from Overcast's own filesystem —
// which is the host's for a native Overcast, and whatever is mounted into
// Overcast's container otherwise (hotReloadVisibilityDiagnostic says when
// that is nothing).
type dirTree string

// sourceFileMax bounds one file read from a mount. A zip entry is bounded
// by the package; a mounted tree could hold anything, and a file past this
// is not source.
const sourceFileMax = 32 << 20

// list walks the mount with the fingerprint's bounds and skip list: the same
// node_modules that would swamp the fingerprint swamps a file picker.
func (d dirTree) list() []sourceFile {
	var files []sourceFile
	budget := hotReloadWalkMaxEntries
	var walk func(dir, rel string, depth int)
	walk = func(dir, rel string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if budget <= 0 {
				return
			}
			budget--
			name := e.Name()
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			if e.IsDir() {
				if _, skip := hotReloadSkipDirs[name]; skip || depth >= hotReloadWalkMaxDepth {
					continue
				}
				walk(filepath.Join(dir, name), childRel, depth+1)
				continue
			}
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			files = append(files, sourceFile{Name: childRel, Size: info.Size()})
		}
	}
	walk(string(d), "", 0)
	return files
}

// read returns the file at name, resolved inside the mount: the name is
// cleaned as a rooted path first, so ".." can climb no higher than the mount
// itself.
func (d dirTree) read(name string) (string, bool) {
	rel := path.Clean("/" + filepath.ToSlash(name))
	if rel == "/" {
		return "", false
	}
	full := filepath.Join(string(d), filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil || !info.Mode().IsRegular() || info.Size() > sourceFileMax {
		return "", false
	}
	content, err := os.ReadFile(full)
	if err != nil {
		return "", false
	}
	return string(content), true
}

// mountedSourceRoot is the host directory a hot-reload function runs from —
// its /var/task — or "" for a function that runs its package.
func (h *Handler) mountedSourceRoot(fn *Function) string {
	normalized, err := hotReloadBindPath(fn, h.cfg.LambdaHotReload)
	if err != nil || normalized == "" {
		return ""
	}
	return hotReloadLocalPath(hotReloadTagPath(fn), normalized)
}

// GetFunctionSource handles GET /_overcast/lambda/functions/{name}/source.
// Returns the handler's source (or a default stub if none stored yet) and the
// file list, or with ?file=<path> that one file — from the deployment zip, or
// from the hot-reload mount the function runs from instead. See the file
// header for the shapes.
func (h *Handler) GetFunctionSource(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	fn, aerr := h.ls.getFunction(r.Context(), name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if fn == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Function not found: " + name,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	// The package is stored apart from the record; the source viewer is one of
	// the few readers of the actual bytes.
	if aerr := h.ls.loadFunctionCode(r.Context(), fn); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	// The files the function runs from: the mount when it has one, else the
	// package.
	var tree sourceTree
	mounted := false
	if root := h.mountedSourceRoot(fn); root != "" {
		tree, mounted = dirTree(root), true
	} else if len(fn.CodeZip) > 0 {
		tree = zipTree(fn.CodeZip)
	}
	var files []sourceFile
	if tree != nil {
		files = tree.list()
	}

	// If a specific file was requested, read it from the tree.
	if reqFile := r.URL.Query().Get("file"); reqFile != "" {
		if tree == nil {
			protocol.WriteJSONError(w, r, protocol.ErrInvalidArgument("no deployment package"))
			return
		}
		content, ok := tree.read(reqFile)
		if !ok {
			protocol.WriteJSONError(w, r, &protocol.AWSError{
				Code:       "ResourceNotFoundException",
				Message:    "File not found in deployment package: " + reqFile,
				HTTPStatus: http.StatusNotFound,
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(sourceResponse{
			Source:   content,
			Filename: reqFile,
			Language: languageForFilename(reqFile, fn.Runtime),
			Files:    files,
		})
		return
	}

	// A mounted function's handler is read from the mount — the package, if
	// it even has one, is not what runs. An unreadable mount (Overcast in a
	// container without it) falls through to the package's own answer.
	if mounted {
		if entry := guessEntryFile(files, fn.Handler, fn.Runtime); entry != "" {
			if content, ok := tree.read(entry); ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(sourceResponse{
					Source:   content,
					Filename: entry,
					Language: languageForFilename(entry, fn.Runtime),
					Files:    files,
				})
				return
			}
		}
	}

	source, filename, placeholder := resolveSource(fn, files)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(sourceResponse{
		Source:      source,
		Filename:    filename,
		Language:    runtimeLanguage(fn.Runtime),
		Files:       files,
		Placeholder: placeholder,
	})
}

// PutFunctionSource handles PUT /_overcast/lambda/functions/{name}/source.
// Stores the source text, packs it into a zip, and updates the function.
func (h *Handler) PutFunctionSource(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	name := chi.URLParam(r, "name")
	ctx := r.Context()

	fn, aerr := h.ls.getFunction(ctx, name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if fn == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Function not found: " + name,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}
	snapshotRevision := fn.RevisionId

	// Patching a single file needs the existing archive's other entries.
	if aerr := h.ls.loadFunctionCode(ctx, fn); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	var req sourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInvalidArgument("invalid request body"))
		return
	}
	if req.Source == "" {
		protocol.WriteJSONError(w, r, protocol.ErrMissingParameter("source"))
		return
	}
	if req.Filename == "" {
		req.Filename = fn.SourceFilename
		if req.Filename == "" {
			_, req.Filename = defaultSourceForRuntime(fn.Runtime, fn.Handler)
		}
	}

	zipBytes, err := func() ([]byte, error) {
		// If the function already has a multi-file zip, patch the single file
		// instead of replacing the entire archive.
		if len(fn.CodeZip) > 0 {
			existing := listZipFiles(fn.CodeZip)
			if len(existing) > 1 {
				return patchZipFile(fn.CodeZip, req.Filename, req.Source)
			}
		}
		return packSourceAsZip(req.Filename, req.Source)
	}()
	if err != nil {
		log.Error("put function source: zip packaging failed",
			zap.String("function", name), zap.Error(err))
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}

	fn, changed, aerr := h.ls.mutateFunction(ctx, name, func(current *Function) (bool, *protocol.AWSError) {
		// The zip was prepared from snapshotRevision. Never apply it to a newer
		// deployment package, where it would restore stale bytes and metadata.
		if current.RevisionId != snapshotRevision {
			return false, &protocol.AWSError{
				Code:       "ResourceConflictException",
				Message:    "The function was modified while its source was being updated.",
				HTTPStatus: http.StatusConflict,
			}
		}
		current.SourceCode = req.Source
		current.SourceFilename = req.Filename
		current.setCode(zipBytes)
		current.CodeS3Bucket = ""
		current.CodeS3Key = ""
		current.CodeS3ObjectVersion = ""
		current.ImageUri = ""
		current.RevisionId = uuid.NewString()
		current.LastModified = h.clk.Now().UTC().Format(time.RFC3339)
		return true, nil
	})
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if !changed || fn == nil {
		protocol.WriteJSONError(w, r, lambdaFunctionNotFound(name))
		return
	}

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{
			Type:    events.LambdaFunctionUpdated,
			Time:    h.clk.Now(),
			Source:  "lambda",
			Payload: events.LambdaFunctionPayload{Name: fn.Name, ARN: fn.ARN},
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(sourceResponse{
		Source:   fn.SourceCode,
		Filename: fn.SourceFilename,
		Language: runtimeLanguage(fn.Runtime),
		Files:    listZipFiles(fn.CodeZip),
	})
}
