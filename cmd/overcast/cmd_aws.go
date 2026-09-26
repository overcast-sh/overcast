package main

// cmd_aws.go — `overcast aws <args...>`. Runs the host `aws` CLI (never
// bundled — resolved via exec.LookPath) against a running overcast, with the
// ambient AWS environment scrubbed first. This is scripts/awslocal.sh and
// scripts/awslocal.ps1 ported into the binary: same rationale (a developer
// machine usually carries AWS_PROFILE, AWS_REGION, SSO state or a stale
// AWS_ENDPOINT_URL that silently changes what an `aws` call does), same fix
// (unset every AWS_* variable, not a curated list, so a future SDK release
// needs no update here), just without the "activate a shell wrapper first"
// step.
//
// DisableFlagParsing is set so every argument after "aws" — including
// AWS-CLI-native flags like --debug or --profile — passes through to the
// child verbatim; overcast's own flag parser never gets a chance to
// misinterpret them. The one piece of overcast's own surface that can still
// appear on this command line is the root --endpoint flag, recognized only
// in the leading position. Cobra hands this command identical args whether
// the flag was written before or after "aws" (it strips just the "aws"
// token while locating the subcommand, and DisableFlagParsing means it
// never parses the flag into its Value either way), so both spellings —
// `overcast --endpoint X aws ...` and `overcast aws --endpoint X ...` —
// resolve the overcast endpoint (see extractLeadingEndpointFlag). That is
// deliberate: the aws CLI has no --endpoint global flag of its own (its
// spelling is --endpoint-url), so a leading --endpoint was always meant
// for overcast. --endpoint-url and anything non-leading pass through
// untouched.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/overcast-sh/overcast/internal/hostbridge/trust"
)

// caFetchTimeout bounds the CA bootstrap fetch below. Short, unlike
// trust.FetchRemoteCA's 10s: this runs on every `overcast aws` invocation
// against an https endpoint, not just an explicit one-off `trust install`,
// so a dead daemon must fail fast rather than stall every AWS call.
const caFetchTimeout = 3 * time.Second

// maxCAPemBytes bounds the fetch: a CA certificate PEM is a couple of KiB;
// anything near this is not one. Mirrors trust.maxCAPemBytes (unexported).
const maxCAPemBytes = 64 * 1024

// caCacheFreshFor is how long a cached CA is served without re-fetching.
// The CA changes essentially never (only `overcast trust` regeneration), so
// this exists purely to keep a per-invocation HTTP round trip — and the
// caFetchTimeout stall when the daemon is down — off every https `overcast
// aws` call. Kept short anyway: a stale-but-wrong CA only costs one
// re-fetch five minutes later.
const caCacheFreshFor = 5 * time.Minute

func newAWSCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "aws [args...]",
		Short: "Run the host AWS CLI against overcast, with the AWS environment scrubbed first",
		Long: "Run the host AWS CLI against overcast, with the AWS environment scrubbed first.\n\n" +
			"Every AWS_* environment variable is unset (not just a curated list) and\n" +
			"replaced with the four an overcast call needs: dummy credentials, region,\n" +
			"and the overcast endpoint. This avoids the class of bug where a leftover\n" +
			"AWS_PROFILE or AWS_ENDPOINT_URL from something else silently redirects\n" +
			"or re-signs the call.\n\n" +
			"All arguments are passed through to `aws` verbatim, including flags such\n" +
			"as --debug. The root --endpoint flag is recognized in the leading position\n" +
			"(overcast aws --endpoint http://localhost:4580 s3 ls, or before \"aws\");\n" +
			"otherwise the OVERCAST_ENDPOINT / OVERCAST_PORT environment variables\n" +
			"apply, same as scripts/awslocal.sh.",
		DisableFlagParsing: true,
		SilenceUsage:       true,
		RunE:               runAWS,
		// With DisableFlagParsing set, Cobra routes every token — flags
		// included — to ValidArgsFunction, which is what lets tab
		// completion inside the passthrough args be delegated to the AWS
		// CLI's own completer.
		ValidArgsFunction: completeAWSArgs,
	}
}

// completeAWSArgs delegates completion of the passthrough args to
// aws_completer, which ships with every AWS CLI v2 install and speaks the
// plain COMP_LINE/COMP_POINT protocol (candidates on stdout, one per
// line). This is the only route to completing a *subcommand*'s passthrough
// args: the shell trick thin wrappers use (`complete -C aws_completer
// awslocal`) keys off the first word of the line, which here is "overcast"
// and already owned by Cobra's completion script.
//
// Without aws_completer on PATH this falls back to file completion — for
// an unknown suffix of an aws command line, a path is the least-wrong
// guess (fileb:// payloads, template paths). Failures do the same: a
// completion function must never surface an error at the prompt.
func completeAWSArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	completer, err := exec.LookPath("aws_completer")
	if err != nil {
		return nil, cobra.ShellCompDirectiveDefault
	}

	// The overcast-owned tokens (a leading --endpoint pair, a "--"
	// separator) are not part of the aws command line being completed.
	_, _, rest := extractLeadingEndpointFlag(args)
	rest = stripLeadingDoubleDash(rest)
	line := awsCompLine(rest, toComplete)

	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, completer)
	c.Env = append(os.Environ(),
		"COMP_LINE="+line,
		fmt.Sprintf("COMP_POINT=%d", len(line)),
	)
	out, err := c.Output()
	if err != nil {
		return nil, cobra.ShellCompDirectiveDefault
	}

	var candidates []string
	for _, cand := range strings.Split(string(out), "\n") {
		if cand = strings.TrimSpace(cand); cand != "" {
			candidates = append(candidates, cand)
		}
	}
	if len(candidates) == 0 {
		return nil, cobra.ShellCompDirectiveDefault
	}
	return candidates, cobra.ShellCompDirectiveNoFileComp
}

// awsCompLine reconstructs the COMP_LINE aws_completer expects: the line as
// if the user had typed `aws` directly. An empty toComplete still counts as
// a (started, empty) final word, which the trailing separator from
// strings.Join naturally encodes.
func awsCompLine(rest []string, toComplete string) string {
	words := append([]string{"aws"}, rest...)
	words = append(words, toComplete)
	return strings.Join(words, " ")
}

// awsExitError carries a child process's exit code through the normal Go
// error path so the caller can propagate it exactly via os.Exit. Only the
// Windows exec path (cmd_aws_exec_windows.go) ever produces one: the unix
// path (cmd_aws_exec.go) replaces the process image with syscall.Exec, so a
// nonzero aws exit status is never seen by this process at all — it *is*
// this process's exit status.
type awsExitError struct{ code int }

func (e *awsExitError) Error() string { return fmt.Sprintf("aws exited with status %d", e.code) }

func runAWS(cmd *cobra.Command, args []string) error {
	binPath, err := exec.LookPath("aws")
	if err != nil {
		return errors.New("aws CLI not found on PATH — install it from https://docs.aws.amazon.com/cli/ or use 'overcast env' to configure another tool")
	}

	endpointOverride, overrideGiven, rest := extractLeadingEndpointFlag(args)
	rest = stripLeadingDoubleDash(rest)

	endpoint := resolveAWSEndpoint(cmd, endpointOverride, overrideGiven)
	region := resolveAWSRegion()

	configPath, err := createEmptyAWSConfigFile()
	if err != nil {
		return err
	}
	defer os.Remove(configPath) //nolint:errcheck // best-effort; see the awsExitError branch below for the one path that must clean up explicitly

	env, err := buildAWSEnv(os.Environ(), endpoint, region, configPath, resolveCABundle(cmd, endpoint))
	if err != nil {
		return err
	}

	execErr := execAWS(binPath, rest, env)
	var exitErr *awsExitError
	if errors.As(execErr, &exitErr) {
		// os.Exit skips deferred calls, so clean up explicitly before it.
		os.Remove(configPath) //nolint:errcheck
		os.Exit(exitErr.code)
	}
	return execErr
}

// extractLeadingEndpointFlag pulls a leading "--endpoint VALUE" or
// "--endpoint=VALUE" off the front of args. Cobra removes only the "aws"
// token while locating this command, so a --endpoint written before the
// subcommand arrives here in the leading position — indistinguishable from
// one written directly after "aws", which is why both spellings work (see
// the file comment). Anything past the leading position is passthrough;
// DisableFlagParsing means Cobra never parses these tokens itself.
func extractLeadingEndpointFlag(args []string) (value string, given bool, rest []string) {
	if len(args) == 0 {
		return "", false, args
	}
	switch {
	case args[0] == "--endpoint" && len(args) >= 2:
		return args[1], true, args[2:]
	case strings.HasPrefix(args[0], "--endpoint="):
		return strings.TrimPrefix(args[0], "--endpoint="), true, args[1:]
	}
	return "", false, args
}

// stripLeadingDoubleDash drops one leading "--" separator, if present,
// before the args are handed to the aws CLI. A user disambiguating their
// own args from overcast's (`overcast aws -- s3 ls`) would otherwise pass a
// stray "--" through to aws.
func stripLeadingDoubleDash(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

// resolveAWSEndpoint applies the flag-value > OVERCAST_ENDPOINT >
// OVERCAST_PORT > flag-default precedence scripts/awslocal.sh and
// scripts/awslocal.ps1 use, given an explicit override extracted from a
// leading --endpoint (see extractLeadingEndpointFlag) since Cobra itself
// never parses that flag on this DisableFlagParsing command.
func resolveAWSEndpoint(cmd *cobra.Command, override string, overrideGiven bool) string {
	f := cmd.Flags().Lookup("endpoint")
	current, defaultValue := "", ""
	if f != nil {
		current, defaultValue = f.Value.String(), f.DefValue
	}
	return resolveAWSEndpointValue(current, defaultValue, override, overrideGiven)
}

// resolveAWSEndpointValue is the pure decision behind resolveAWSEndpoint,
// factored out so the precedence can be unit tested without building a
// cobra.Command.
func resolveAWSEndpointValue(currentFlagValue, defaultFlagValue, override string, overrideGiven bool) string {
	if overrideGiven {
		return override
	}
	if currentFlagValue != defaultFlagValue {
		// The flag was actually parsed to a non-default value — not
		// reachable today given DisableFlagParsing, but honored in case
		// that ever changes upstream.
		return currentFlagValue
	}
	if v := os.Getenv("OVERCAST_ENDPOINT"); v != "" {
		return v
	}
	if v := os.Getenv("OVERCAST_PORT"); v != "" {
		return "http://localhost:" + v
	}
	return currentFlagValue
}

// resolveAWSRegion applies the OVERCAST_REGION > us-east-1 precedence
// scripts/awslocal.sh and scripts/awslocal.ps1 use.
func resolveAWSRegion() string {
	if v := os.Getenv("OVERCAST_REGION"); v != "" {
		return v
	}
	return "us-east-1"
}

// createEmptyAWSConfigFile creates the empty file AWS_CONFIG_FILE and
// AWS_SHARED_CREDENTIALS_FILE both point at, so a profile is never resolved
// from ~/.aws even when no AWS_PROFILE is set (mirrors scripts/awslocal.sh's
// mktemp; unlike the shell scripts we don't need the cygpath dance, since
// this binary IS the native process, not something Git Bash's `aws` shim has
// to translate a path for).
func createEmptyAWSConfigFile() (string, error) {
	f, err := os.CreateTemp("", "overcast-aws-config-*")
	if err != nil {
		return "", fmt.Errorf("aws: create empty AWS config file: %w", err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("aws: close empty AWS config file: %w", err)
	}
	return path, nil
}

// buildAWSEnv builds the child process environment from environ (normally
// os.Environ()): every AWS_* variable is dropped, then the fixed set an
// overcast call needs is appended. Pure and independent of the OS process,
// so tests can drive it with a fabricated environ.
func buildAWSEnv(environ []string, endpoint, region, configFile, caBundle string) ([]string, error) {
	if endpoint == "" {
		return nil, errors.New("aws: empty endpoint")
	}
	if configFile == "" {
		return nil, errors.New("aws: empty AWS config file path")
	}
	if region == "" {
		region = "us-east-1"
	}

	out := make([]string, 0, len(environ)+10)
	for _, kv := range environ {
		if strings.HasPrefix(kv, "AWS_") {
			continue
		}
		out = append(out, kv)
	}

	out = append(out,
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"AWS_DEFAULT_REGION="+region,
		"AWS_REGION="+region,
		"AWS_ENDPOINT_URL="+endpoint,
		"AWS_PAGER=",
		"AWS_EC2_METADATA_DISABLED=true",
		"AWS_CONFIG_FILE="+configFile,
		"AWS_SHARED_CREDENTIALS_FILE="+configFile,
	)
	if caBundle != "" {
		out = append(out, "AWS_CA_BUNDLE="+caBundle)
	}
	return out, nil
}

// resolveCABundle is the path of a cached copy of an https endpoint's CA,
// or "" for an http endpoint or when there is none to be had, in which case
// the reason goes to stderr.
func resolveCABundle(cmd *cobra.Command, endpoint string) string {
	if !isHTTPSEndpoint(endpoint) {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "overcast: could not resolve home directory for the overcast CA cache: %v\n", err)
		return ""
	}
	path, warning := ensureCABundle(cmd.Context(), endpoint, home, nil)
	if warning != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), warning)
	}
	return path
}

func isHTTPSEndpoint(endpoint string) bool {
	return strings.HasPrefix(strings.ToLower(endpoint), "https://")
}

// caCachePath is where a fetched CA for endpoint is cached:
// <homeDir>/.overcast/ca/<sha256(endpoint)>.pem. Keyed by the endpoint's
// full text (not just host:port) so http and https spellings of the same
// daemon do not collide — an unlikely mix-up, but the hash is free either
// way. Deliberately separate from internal/hostbridge/trust's own cache
// (<dataDir>/ca-remote/<host_port>/ca.crt, used by `overcast trust`): that
// one is a trust-store install target with install/uninstall lifecycle
// tracked against it, this one is just a fetch-and-reuse cache for a single
// process's AWS_CA_BUNDLE and has no business sharing that directory.
func caCachePath(homeDir, endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return filepath.Join(homeDir, ".overcast", "ca", hex.EncodeToString(sum[:])+".pem")
}

// ensureCABundle returns the path of a cached copy of endpoint's CA
// certificate, fetching it (see the file comment on fetchCABundle for why
// the fetch itself is unverified) into caCachePath(homeDir, endpoint) when
// the cache is missing or older than caCacheFreshFor. A fresh cache entry
// short-circuits without any network I/O. A failed re-fetch falls back to a
// stale cache entry; if neither is available it returns a one-line warning
// instead of an error, since a missing CA bundle degrades to "TLS
// verification may fail" rather than blocking the command entirely — the
// caller should print the warning and carry on without AWS_CA_BUNDLE.
//
// client is normally nil (the real insecure bootstrap client is built
// internally); tests pass one pointed at a local test server.
func ensureCABundle(ctx context.Context, endpoint, homeDir string, client *http.Client) (path, warning string) {
	path = caCachePath(homeDir, endpoint)

	if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) < caCacheFreshFor {
		return path, ""
	}

	body, fetchErr := fetchCABundle(ctx, endpoint, client)
	if fetchErr == nil {
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr == nil {
			if wErr := os.WriteFile(path, body, 0o644); wErr == nil {
				return path, ""
			}
		}
	}

	if _, statErr := os.Stat(path); statErr == nil {
		return path, ""
	}

	if fetchErr == nil {
		fetchErr = fmt.Errorf("could not write CA cache at %s", path)
	}
	return "", fmt.Sprintf(
		"overcast: could not obtain the overcast CA for %s (%v) — HTTPS aws calls may fail TLS verification; run 'overcast trust install --endpoint %s' or retry once the daemon is reachable",
		endpoint, fetchErr, endpoint)
}

// fetchCABundle GETs endpoint+trust.CAPemPath. TLS verification is
// deliberately disabled for this one request, same rationale as
// internal/hostbridge/trust/remote.go: the whole point of the fetch is to
// establish trust in the first place, so verifying it against that same
// not-yet-established trust would prove nothing. This is bounded to
// loopback/self-controlled endpoints by the same expectation trust install
// relies on (overcast's own --endpoint targets a daemon the caller chose).
func fetchCABundle(ctx context.Context, endpoint string, client *http.Client) ([]byte, error) {
	if client == nil {
		client = &http.Client{
			Timeout: caFetchTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // see func comment
			},
		}
	}

	ctx, cancel := context.WithTimeout(ctx, caFetchTimeout)
	defer cancel()

	url := strings.TrimRight(endpoint, "/") + trust.CAPemPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build CA request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: unexpected status %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCAPemBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if len(body) > maxCAPemBytes {
		return nil, fmt.Errorf("response from %s exceeds %d bytes — not a CA certificate", url, maxCAPemBytes)
	}
	return body, nil
}
