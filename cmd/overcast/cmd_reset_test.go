package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// resetTestRoot builds a minimal root command carrying the same persistent
// --endpoint flag main.go registers, with newResetCmd() attached — so RunE's
// cmd.Flags().GetString("endpoint") resolves the way it does through the
// real CLI tree. Mirrors servicesTestRoot in cmd_services_test.go.
func resetTestRoot() (*cobra.Command, *bytes.Buffer) {
	root := &cobra.Command{Use: "overcast", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().String("endpoint", "http://localhost:4566", "overcast daemon base URL")
	root.AddCommand(newResetCmd())
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	return root, buf
}

// withResetStdinIsTerminal swaps the resetStdinIsTerminal seam for the
// duration of one test, restoring it on cleanup. Mirrors withTLSSeams in
// tls_settings_test.go.
func withResetStdinIsTerminal(t *testing.T, tty bool) {
	t.Helper()
	prev := resetStdinIsTerminal
	resetStdinIsTerminal = func() bool { return tty }
	t.Cleanup(func() { resetStdinIsTerminal = prev })
}

// resetFixtureServer records every request it receives and answers with a
// canned 200 {"status":"reset"[,"service":...]} body, mirroring
// internal/router/reset.go's response shape exactly.
type resetFixtureServer struct {
	*httptest.Server
	requests []*http.Request
}

func newResetFixtureServer(t *testing.T) *resetFixtureServer {
	t.Helper()
	rfs := &resetFixtureServer{}
	rfs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rfs.requests = append(rfs.requests, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if strings.HasPrefix(r.URL.Path, "/_overcast/reset/") {
			service := strings.TrimPrefix(r.URL.Path, "/_overcast/reset/")
			_, _ = w.Write([]byte(`{"status":"reset","service":"` + service + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"reset"}`))
	}))
	t.Cleanup(rfs.Close)
	return rfs
}

// TestResetCmd_nonTTYWipesAllStateWithoutPrompting proves the CI/scripts
// path: no seam override needed because a bytes.Buffer stdin is never a
// terminal, so resetStdinIsTerminal (reading the real process's os.Stdin,
// not cmd.InOrStdin()) already reports false in the test binary too — but
// to keep the test independent of the actual test-runner's stdin, it pins
// the seam explicitly.
func TestResetCmd_nonTTYWipesAllStateWithoutPrompting(t *testing.T) {
	withResetStdinIsTerminal(t, false)
	srv := newResetFixtureServer(t)

	root, buf := resetTestRoot()
	root.SetArgs([]string{"reset", "--endpoint", srv.URL})

	if err := root.Execute(); err != nil {
		t.Fatalf("reset: %v (output: %q)", err, buf.String())
	}
	if len(srv.requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(srv.requests))
	}
	req := srv.requests[0]
	if req.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if req.URL.Path != "/_overcast/reset" {
		t.Errorf("path = %q, want /_overcast/reset", req.URL.Path)
	}
	if strings.Contains(buf.String(), "Continue?") {
		t.Errorf("output %q contains a confirmation prompt in the non-TTY path", buf.String())
	}
}

// TestResetCmd_resetsOneService covers the [service] positional argument:
// the request goes to /_overcast/reset/{service}, and the success line names
// the service.
func TestResetCmd_resetsOneService(t *testing.T) {
	withResetStdinIsTerminal(t, false)
	srv := newResetFixtureServer(t)

	root, buf := resetTestRoot()
	root.SetArgs([]string{"reset", "s3", "--endpoint", srv.URL})

	if err := root.Execute(); err != nil {
		t.Fatalf("reset s3: %v (output: %q)", err, buf.String())
	}
	if len(srv.requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(srv.requests))
	}
	if got := srv.requests[0].URL.Path; got != "/_overcast/reset/s3" {
		t.Errorf("path = %q, want /_overcast/reset/s3", got)
	}
	if !strings.Contains(buf.String(), "s3") {
		t.Errorf("output %q does not mention the service", buf.String())
	}
}

// TestResetCmd_yesFlagSkipsPromptEvenOnATTY proves --yes overrides an
// interactive stdin: even with the TTY seam forced true, no prompt is
// printed and the request fires immediately (nothing was fed to stdin, so
// a prompt that ran would block forever waiting for input that isn't there).
func TestResetCmd_yesFlagSkipsPromptEvenOnATTY(t *testing.T) {
	withResetStdinIsTerminal(t, true)
	srv := newResetFixtureServer(t)

	root, buf := resetTestRoot()
	root.SetIn(strings.NewReader("")) // no input available — a prompt would hang
	root.SetArgs([]string{"reset", "--yes", "--endpoint", srv.URL})

	if err := root.Execute(); err != nil {
		t.Fatalf("reset --yes: %v (output: %q)", err, buf.String())
	}
	if len(srv.requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(srv.requests))
	}
	if strings.Contains(buf.String(), "Continue?") {
		t.Errorf("output %q contains a confirmation prompt despite --yes", buf.String())
	}
}

// TestResetCmd_ttyConfirmed proves the interactive path: with the TTY seam
// forced true and "y" fed on stdin, the prompt names what will be wiped and
// the request still fires.
func TestResetCmd_ttyConfirmed(t *testing.T) {
	withResetStdinIsTerminal(t, true)
	srv := newResetFixtureServer(t)

	root, buf := resetTestRoot()
	root.SetIn(strings.NewReader("y\n"))
	root.SetArgs([]string{"reset", "--endpoint", srv.URL})

	if err := root.Execute(); err != nil {
		t.Fatalf("reset (confirmed): %v (output: %q)", err, buf.String())
	}
	if len(srv.requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(srv.requests))
	}
	out := buf.String()
	if !strings.Contains(out, "wipe all emulated state at "+srv.URL) {
		t.Errorf("output %q does not describe what will be wiped", out)
	}
	if !strings.Contains(out, "Continue?") {
		t.Errorf("output %q does not show the confirmation prompt", out)
	}
}

// TestResetCmd_ttyAborted proves declining the prompt (anything but y/yes)
// skips the request entirely.
func TestResetCmd_ttyAborted(t *testing.T) {
	withResetStdinIsTerminal(t, true)
	srv := newResetFixtureServer(t)

	root, buf := resetTestRoot()
	root.SetIn(strings.NewReader("n\n"))
	root.SetArgs([]string{"reset", "--endpoint", srv.URL})

	if err := root.Execute(); err != nil {
		t.Fatalf("reset (declined): %v (output: %q)", err, buf.String())
	}
	if len(srv.requests) != 0 {
		t.Fatalf("got %d requests, want 0 — the reset must not fire after a decline", len(srv.requests))
	}
	if !strings.Contains(buf.String(), "aborted") {
		t.Errorf("output %q does not report the abort", buf.String())
	}
}

// TestResetCmd_ttyServiceConfirmationNamesService proves the per-service
// confirmation text names the service, not just "emulated state".
func TestResetCmd_ttyServiceConfirmationNamesService(t *testing.T) {
	withResetStdinIsTerminal(t, true)
	srv := newResetFixtureServer(t)

	root, buf := resetTestRoot()
	root.SetIn(strings.NewReader("n\n"))
	root.SetArgs([]string{"reset", "dynamodb", "--endpoint", srv.URL})

	if err := root.Execute(); err != nil {
		t.Fatalf("reset dynamodb (declined): %v (output: %q)", err, buf.String())
	}
	if !strings.Contains(buf.String(), "wipe all dynamodb state at "+srv.URL) {
		t.Errorf("output %q does not describe the per-service wipe", buf.String())
	}
}

// TestResetCmd_unreachableDaemon covers a closed connection: the error must
// match cmd_status.go's wording ("overcast unreachable at %s") per the design
// brief, so overcast's commands read consistently.
func TestResetCmd_unreachableDaemon(t *testing.T) {
	withResetStdinIsTerminal(t, false)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // nothing listening at srv.URL

	root, _ := resetTestRoot()
	root.SetArgs([]string{"reset", "--endpoint", srv.URL})

	err := root.Execute()
	if err == nil {
		t.Fatal("reset against a closed server succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "overcast unreachable at "+srv.URL) {
		t.Errorf("error %q does not match the cmd_status.go wording", err)
	}
}

// TestResetCmd_unknownService covers the 400 the daemon returns for an
// unrecognized service name: the CLI surfaces the daemon's own message.
func TestResetCmd_unknownService(t *testing.T) {
	withResetStdinIsTerminal(t, false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unknown service: bogus"}`))
	}))
	defer srv.Close()

	root, _ := resetTestRoot()
	root.SetArgs([]string{"reset", "bogus", "--endpoint", srv.URL})

	err := root.Execute()
	if err == nil {
		t.Fatal("reset bogus succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "unknown service: bogus") {
		t.Errorf("error %q does not surface the daemon's message", err)
	}
}

// TestResetCmd_non200 keeps the failure path honest for an unexpected status
// with no parseable error body.
func TestResetCmd_non200(t *testing.T) {
	withResetStdinIsTerminal(t, false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	root, _ := resetTestRoot()
	root.SetArgs([]string{"reset", "--endpoint", srv.URL})

	if err := root.Execute(); err == nil {
		t.Fatal("reset against a 500 daemon succeeded, want an error")
	}
}

// TestResetCmd_commandShape pins the parts of the command declaration that
// don't need a server: any number of services, --yes registered, and
// completion for [service] falls back to no candidates against an
// unreachable endpoint rather than erroring or hanging.
func TestResetCmd_commandShape(t *testing.T) {
	cmd := newResetCmd()
	if cmd.Args == nil {
		t.Fatal("newResetCmd: Args is nil, want cobra.ArbitraryArgs")
	}
	if err := cmd.Args(cmd, []string{"one", "two", "three"}); err != nil {
		t.Errorf("newResetCmd: Args rejected several services: %v", err)
	}
	if cmd.Flag("yes") == nil {
		t.Fatal("newResetCmd: --yes flag not registered")
	}
	if cmd.ValidArgsFunction == nil {
		t.Fatal("newResetCmd: ValidArgsFunction is nil")
	}

	cmd.Flags().String("endpoint", "http://127.0.0.1:1", "") // deliberately unreachable
	// A real invocation always has a context by the time ValidArgsFunction
	// runs (cobra's ExecuteC sets one) — set it explicitly here since this
	// test calls the function directly against a command that was never
	// executed.
	cmd.SetContext(context.Background())
	candidates, directive := cmd.ValidArgsFunction(cmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("ValidArgsFunction directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
	if candidates != nil {
		t.Errorf("ValidArgsFunction against an unreachable daemon returned %v, want nil", candidates)
	}
}

// TestResetCmd_completionListsEnabledServices proves the happy path: given a
// reachable daemon, [service] completion offers its enabled services.
func TestResetCmd_completionListsEnabledServices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_overcast/health" {
			t.Errorf("unexpected completion path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"services":["s3","sqs"]}`))
	}))
	defer srv.Close()

	cmd := newResetCmd()
	cmd.Flags().String("endpoint", srv.URL, "")
	cmd.SetContext(context.Background())
	candidates, directive := cmd.ValidArgsFunction(cmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
	if len(candidates) != 2 || candidates[0] != "s3" || candidates[1] != "sqs" {
		t.Errorf("candidates = %v, want [s3 sqs]", candidates)
	}
}

// TestResetCmd_completionOmitsServicesAlreadyNamed proves the [service...]
// completer does not offer a service the command line already names.
func TestResetCmd_completionOmitsServicesAlreadyNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"services":["athena","glue","s3"]}`))
	}))
	defer srv.Close()

	cmd := newResetCmd()
	cmd.Flags().String("endpoint", srv.URL, "")
	cmd.SetContext(context.Background())
	candidates, directive := cmd.ValidArgsFunction(cmd, []string{"s3", "athena"}, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
	if len(candidates) != 1 || candidates[0] != "glue" {
		t.Errorf("candidates = %v, want [glue]", candidates)
	}
}

// TestResetCmd_unknownServiceAmongSeveralWipesNothing proves every name is
// checked before the first reset, and a repeated name is reset once.
func TestResetCmd_unknownServiceAmongSeveralWipesNothing(t *testing.T) {
	withResetStdinIsTerminal(t, false)
	srv := newResetFixtureServer(t)

	root, _ := resetTestRoot()
	root.SetArgs([]string{"reset", "athena", "glu", "s3", "--endpoint", srv.URL})
	err := root.Execute()

	if err == nil || !strings.Contains(err.Error(), "unknown service: glu") {
		t.Fatalf("err = %v, want the unknown name", err)
	}
	if len(srv.requests) != 0 {
		t.Fatalf("got %d reset requests, want none", len(srv.requests))
	}

	root, _ = resetTestRoot()
	root.SetArgs([]string{"reset", "s3", "s3", "--endpoint", srv.URL})
	if err := root.Execute(); err != nil || len(srv.requests) != 1 {
		t.Fatalf("reset s3 s3 = %v with %d requests, want one", err, len(srv.requests))
	}
}

// TestResetCmd_resetsSeveralServices covers naming more than one service:
// each is reset in turn, and a confirmation names them all.
func TestResetCmd_resetsSeveralServices(t *testing.T) {
	withResetStdinIsTerminal(t, true)
	srv := newResetFixtureServer(t)

	root, buf := resetTestRoot()
	root.SetIn(strings.NewReader("y\n"))
	root.SetArgs([]string{"reset", "athena", "glue", "s3", "--endpoint", srv.URL})

	if err := root.Execute(); err != nil {
		t.Fatalf("reset athena glue s3: %v (output: %q)", err, buf.String())
	}
	if !strings.Contains(buf.String(), "wipe all athena, glue and s3 state at "+srv.URL) {
		t.Errorf("output %q does not name every service", buf.String())
	}
	var paths []string
	for _, r := range srv.requests {
		paths = append(paths, r.URL.Path)
	}
	if strings.Join(paths, " ") != "/_overcast/reset/athena /_overcast/reset/glue /_overcast/reset/s3" {
		t.Errorf("requests = %v, want one reset per service, in order", paths)
	}
}
