package groups

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// AppConfig returns the AWS AppConfig service group.
//
// Every call here is signed (awscli.RunSigned), where most of this suite runs
// --no-sign-request. AppConfig shares its `/applications` path space with
// Service Catalog AppRegistry, and Overcast can only tell the two apart by the
// SigV4 credential scope: an unsigned `aws appconfig create-application` is
// answered by AppRegistry with a 200 and its own `{"application": …}` body,
// which the CLI silently renders as an empty result. A production caller always
// signs, so signing is both the faithful shape and the only way this suite can
// observe AppConfig at all.
//
// The data plane (StartConfigurationSession, GetLatestConfiguration) is a
// separate AWS model with its own signing name and lives in the appconfigdata
// group, not here.
//
// Groups: appconfig-hosted-versions, appconfig-deployment-strategies,
// appconfig-extensions, appconfig-experiments. appconfig-applications,
// appconfig-environments, appconfig-configuration-profiles, appconfig-tags and
// appconfig-deployments resolve through their authored scenarios
// (compat/model/authored/appconfig-applications.json,
// appconfig-environments.json, appconfig-configuration-profiles.json,
// appconfig-tags.json, appconfig-deployments.json).
func AppConfig() ServiceGroup {
	g := &appconfigGroup{}
	return ServiceGroup{
		// Every key is group-qualified: a bare key that two groups could claim
		// is refused by the loader.
		Impls: map[string]harness.TestFn{
			"appconfig-hosted-versions:CreateHostedConfigurationVersion":             g.CreateHostedConfigurationVersion,
			"appconfig-hosted-versions:GetHostedConfigurationVersion":                g.GetHostedConfigurationVersion,
			"appconfig-hosted-versions:ListHostedConfigurationVersions":              g.ListHostedConfigurationVersions,
			"appconfig-hosted-versions:ListHostedConfigurationVersionsByLabel":       g.ListHostedConfigurationVersionsByLabel,
			"appconfig-hosted-versions:CreateHostedConfigurationVersionStaleVersion": g.CreateHostedConfigurationVersionStaleVersion,
			"appconfig-hosted-versions:DeleteHostedConfigurationVersion":             g.DeleteHostedConfigurationVersion,
			"appconfig-hosted-versions:GetHostedConfigurationVersionNotFound":        g.GetHostedConfigurationVersionNotFound,

			"appconfig-deployment-strategies:CreateDeploymentStrategy": g.CreateDeploymentStrategy,
			"appconfig-deployment-strategies:GetDeploymentStrategy":    g.GetDeploymentStrategy,
			"appconfig-deployment-strategies:ListDeploymentStrategies": g.ListDeploymentStrategies,
			"appconfig-deployment-strategies:UpdateDeploymentStrategy": g.UpdateDeploymentStrategy,
			"appconfig-deployment-strategies:DeleteDeploymentStrategy": g.DeleteDeploymentStrategy,

			"appconfig-extensions:CreateExtension":            g.CreateExtension,
			"appconfig-extensions:GetExtension":               g.GetExtension,
			"appconfig-extensions:ListExtensions":             g.ListExtensions,
			"appconfig-extensions:UpdateExtension":            g.UpdateExtension,
			"appconfig-extensions:DeleteExtension":            g.DeleteExtension,
			"appconfig-extensions:CreateExtensionAssociation": g.CreateExtensionAssociation,
			"appconfig-extensions:GetExtensionAssociation":    g.GetExtensionAssociation,
			"appconfig-extensions:ListExtensionAssociations":  g.ListExtensionAssociations,
			"appconfig-extensions:UpdateExtensionAssociation": g.UpdateExtensionAssociation,
			"appconfig-extensions:DeleteExtensionAssociation": g.DeleteExtensionAssociation,

			"appconfig-experiments:ListExperimentDefinitions": g.ListExperimentDefinitions,
			"appconfig-experiments:ListExperimentRuns":        g.ListExperimentRuns,
			"appconfig-experiments:ListExperimentRunEvents":   g.ListExperimentRunEvents,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"appconfig-hosted-versions": g.setupHostedVersions,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"appconfig-hosted-versions":       g.teardownHostedVersions,
			"appconfig-deployment-strategies": g.teardownDeploymentStrategies,
			"appconfig-extensions":            g.teardownExtensions,
		},
	}
}

type appconfigGroup struct{}

// One namer per group so two groups running in parallel never share a name.
var (
	acHostedNamer   = harness.NewNamer("ac-hcv")
	acStrategyNamer = harness.NewNamer("ac-ds")
	acExtNamer      = harness.NewNamer("ac-ext")
)

// acMissingID stands in for an identifier no resource has. AppConfig's model
// constrains Id to `[a-z0-9]{4,7}`, so a probe has to look like one or the
// request is rejected before it reaches a handler.
const acMissingID = "zzzzzzz"

// ─── Helpers ──────────────────────────────────────────────────────────────────

func acRun(t *harness.TestContext, args ...string) error {
	return awscli.RunSigned(t.Endpoint, t.Region, append([]string{"appconfig"}, args...)...)
}

func acOut(t *harness.TestContext, args ...string) (map[string]any, error) {
	return awscli.RunOutputSigned(t.Endpoint, t.Region, append([]string{"appconfig"}, args...)...)
}

// The HTTP statuses the pinned AppConfig model binds to its error shapes.
// Every negative path asserts one of these alongside the error code: the code
// on its own would not notice a status move, which is what alpha.35 did to
// several other services.
const (
	acStatusNotFound = 404 // ResourceNotFoundException
	acStatusConflict = 409 // ConflictException
)

// acExpectFailure requires the command to fail with the given AWS error code
// and HTTP status. It signs, because an unsigned AppConfig request is answered
// by AppRegistry — the status read back would be that service's.
func acExpectFailure(t *harness.TestContext, what, code string, status int, args ...string) error {
	return assertAWSFailure(awscli.RunStatusSigned, t, "appconfig "+what, code, status,
		append([]string{"appconfig"}, args...)...)
}

// acARN builds an AppConfig resource ARN. Overcast's tag dispatcher picks the
// owning service out of the ARN, so the service field has to be "appconfig" or
// the request is answered by API Gateway's service-agnostic tag store.
func acARN(t *harness.TestContext, resource string) string {
	return fmt.Sprintf("arn:aws:appconfig:%s:000000000000:%s", t.Region, resource)
}

func acString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// acItems returns the Items member every AppConfig list operation answers with.
func acItems(out map[string]any) []map[string]any {
	raw, _ := out["Items"].([]any)
	items := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if m, ok := v.(map[string]any); ok {
			items = append(items, m)
		}
	}
	return items
}

// acFindByID returns the item whose Id (or VersionNumber, rendered) matches.
func acFindByID(items []map[string]any, key, want string) map[string]any {
	for _, it := range items {
		if fmt.Sprintf("%v", it[key]) == want {
			return it
		}
	}
	return nil
}

// acTempFile writes content to a throwaway file and returns its path plus a
// cleanup func. The hosted-configuration-version operations bind their payload
// as a blob, so the CLI reads it from a file and writes the response payload to
// a positional outfile.
func acTempFile(name, content string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "oc-appconfig-")
	if err != nil {
		return "", func() {}, fmt.Errorf("appconfig: temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(dir) } //nolint:errcheck
	path := filepath.Join(dir, name)
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("appconfig: write %s: %w", name, err)
		}
	}
	return path, cleanup, nil
}

// acCreateApp creates an application and returns its id.
func acCreateApp(t *harness.TestContext, name string) (string, error) {
	out, err := acOut(t, "create-application", "--name", name)
	if err != nil {
		return "", err
	}
	id := acString(out, "Id")
	if id == "" {
		return "", fmt.Errorf("appconfig create-application %q: no Id in response: %v", name, out)
	}
	return id, nil
}

// acDeleteApp removes an application, ignoring failures — teardown only.
func acDeleteApp(t *harness.TestContext, key string) {
	if id := t.GetString(key); id != "" {
		acRun(t, "delete-application", "--application-id", id) //nolint:errcheck
	}
}

// ─── appconfig-hosted-versions ────────────────────────────────────────────────

const acHostedContent = `{"compat":true,"level":"info"}`

func (g *appconfigGroup) setupHostedVersions(_ context.Context, t *harness.TestContext) error {
	appID, err := acCreateApp(t, acHostedNamer.Name(t))
	if err != nil {
		return err
	}
	t.Set("ac_hcv_app_id", appID)
	out, err := acOut(t, "create-configuration-profile",
		"--application-id", appID, "--name", acHostedNamer.Name(t), "--location-uri", "hosted")
	if err != nil {
		return err
	}
	profID := acString(out, "Id")
	if profID == "" {
		return fmt.Errorf("appconfig hosted-versions setup: no profile Id in response: %v", out)
	}
	t.Set("ac_hcv_prof_id", profID)
	return nil
}

func (g *appconfigGroup) teardownHostedVersions(_ context.Context, t *harness.TestContext) error {
	appID, profID := t.GetString("ac_hcv_app_id"), t.GetString("ac_hcv_prof_id")
	if appID != "" && profID != "" {
		// Versions are not cascaded by the profile delete, so remove every one
		// the group could have created before the profile goes.
		for _, version := range []string{"1", "2", "3"} {
			acRun(t, "delete-hosted-configuration-version", //nolint:errcheck
				"--application-id", appID, "--configuration-profile-id", profID, "--version-number", version)
		}
		acRun(t, "delete-configuration-profile", //nolint:errcheck
			"--application-id", appID, "--configuration-profile-id", profID)
	}
	acDeleteApp(t, "ac_hcv_app_id")
	return nil
}

// acCreateHostedVersion posts content to the profile and returns the parsed
// metadata plus the bytes the CLI wrote to the outfile.
func acCreateHostedVersion(t *harness.TestContext, content string, extra ...string) (map[string]any, string, error) {
	in, cleanIn, err := acTempFile("content.json", content)
	if err != nil {
		return nil, "", err
	}
	defer cleanIn()
	outPath, cleanOut, err := acTempFile("payload.json", "")
	if err != nil {
		return nil, "", err
	}
	defer cleanOut()

	args := append([]string{
		"create-hosted-configuration-version",
		"--application-id", t.GetString("ac_hcv_app_id"),
		"--configuration-profile-id", t.GetString("ac_hcv_prof_id"),
		"--content", "fileb://" + in,
		"--content-type", "application/json",
	}, extra...)
	args = append(args, outPath)

	out, err := acOut(t, args...)
	if err != nil {
		return nil, "", err
	}
	written, readErr := os.ReadFile(outPath)
	if readErr != nil {
		return nil, "", fmt.Errorf("appconfig create-hosted-configuration-version: reading the outfile failed: %w", readErr)
	}
	return out, string(written), nil
}

func (g *appconfigGroup) CreateHostedConfigurationVersion(_ context.Context, t *harness.TestContext) error {
	out, payload, err := acCreateHostedVersion(t, acHostedContent,
		"--version-label", "compat-v1", "--description", "compat hosted versions group")
	if err != nil {
		return err
	}
	if got := fmt.Sprintf("%v", out["VersionNumber"]); got != "1" {
		return fmt.Errorf("appconfig CreateHostedConfigurationVersion: expected VersionNumber 1, got %v", out["VersionNumber"])
	}
	if acString(out, "ContentType") != "application/json" || acString(out, "VersionLabel") != "compat-v1" {
		return fmt.Errorf("appconfig CreateHostedConfigurationVersion: expected ContentType application/json and VersionLabel compat-v1, got %v", out)
	}
	if payload != acHostedContent {
		return fmt.Errorf("appconfig CreateHostedConfigurationVersion: expected the content back as the payload (%q), got %q", acHostedContent, payload)
	}
	return nil
}

func (g *appconfigGroup) GetHostedConfigurationVersion(_ context.Context, t *harness.TestContext) error {
	outPath, clean, err := acTempFile("payload.json", "")
	if err != nil {
		return err
	}
	defer clean()
	out, err := acOut(t, "get-hosted-configuration-version",
		"--application-id", t.GetString("ac_hcv_app_id"),
		"--configuration-profile-id", t.GetString("ac_hcv_prof_id"),
		"--version-number", "1",
		outPath,
	)
	if err != nil {
		return err
	}
	if got := fmt.Sprintf("%v", out["VersionNumber"]); got != "1" {
		return fmt.Errorf("appconfig GetHostedConfigurationVersion: expected VersionNumber 1, got %v", out["VersionNumber"])
	}
	if acString(out, "VersionLabel") != "compat-v1" {
		return fmt.Errorf("appconfig GetHostedConfigurationVersion: expected VersionLabel compat-v1, got %v", out)
	}
	payload, err := os.ReadFile(outPath)
	if err != nil {
		return fmt.Errorf("appconfig GetHostedConfigurationVersion: reading the outfile failed: %w", err)
	}
	if string(payload) != acHostedContent {
		return fmt.Errorf("appconfig GetHostedConfigurationVersion: expected content %q, got %q", acHostedContent, payload)
	}
	return nil
}

func (g *appconfigGroup) ListHostedConfigurationVersions(_ context.Context, t *harness.TestContext) error {
	out, err := acOut(t, "list-hosted-configuration-versions",
		"--application-id", t.GetString("ac_hcv_app_id"),
		"--configuration-profile-id", t.GetString("ac_hcv_prof_id"))
	if err != nil {
		return err
	}
	items := acItems(out)
	found := acFindByID(items, "VersionNumber", "1")
	if found == nil {
		return fmt.Errorf("appconfig ListHostedConfigurationVersions: version 1 absent from %d items", len(items))
	}
	if acString(found, "VersionLabel") != "compat-v1" || acString(found, "ContentType") != "application/json" {
		return fmt.Errorf("appconfig ListHostedConfigurationVersions: expected the v1 summary to carry VersionLabel compat-v1 and ContentType application/json, got %v", found)
	}
	return nil
}

func (g *appconfigGroup) ListHostedConfigurationVersionsByLabel(_ context.Context, t *harness.TestContext) error {
	if _, _, err := acCreateHostedVersion(t, `{"compat":true,"level":"debug"}`, "--version-label", "compat-v2"); err != nil {
		return fmt.Errorf("appconfig ListHostedConfigurationVersionsByLabel: creating the second version failed: %w", err)
	}
	out, err := acOut(t, "list-hosted-configuration-versions",
		"--application-id", t.GetString("ac_hcv_app_id"),
		"--configuration-profile-id", t.GetString("ac_hcv_prof_id"),
		"--version-label", "compat-v2")
	if err != nil {
		return err
	}
	items := acItems(out)
	if acFindByID(items, "VersionNumber", "2") == nil {
		return fmt.Errorf("appconfig ListHostedConfigurationVersionsByLabel: version 2 absent from the filtered page (%d items)", len(items))
	}
	if acFindByID(items, "VersionNumber", "1") != nil {
		return fmt.Errorf("appconfig ListHostedConfigurationVersionsByLabel: the version_label filter also returned version 1")
	}
	return nil
}

func (g *appconfigGroup) CreateHostedConfigurationVersionStaleVersion(_ context.Context, t *harness.TestContext) error {
	in, clean, err := acTempFile("content.json", acHostedContent)
	if err != nil {
		return err
	}
	defer clean()
	outPath, cleanOut, err := acTempFile("payload.json", "")
	if err != nil {
		return err
	}
	defer cleanOut()
	// A Latest-Version-Number that is not the profile's latest is
	// ConflictException — 409.
	return acExpectFailure(t, "CreateHostedConfigurationVersionStaleVersion", "ConflictException", acStatusConflict,
		"create-hosted-configuration-version",
		"--application-id", t.GetString("ac_hcv_app_id"),
		"--configuration-profile-id", t.GetString("ac_hcv_prof_id"),
		"--content", "fileb://"+in,
		"--content-type", "application/json",
		"--latest-version-number", "9999",
		outPath,
	)
}

func (g *appconfigGroup) DeleteHostedConfigurationVersion(_ context.Context, t *harness.TestContext) error {
	appID, profID := t.GetString("ac_hcv_app_id"), t.GetString("ac_hcv_prof_id")
	if err := acRun(t, "delete-hosted-configuration-version",
		"--application-id", appID, "--configuration-profile-id", profID, "--version-number", "1"); err != nil {
		return err
	}
	outPath, clean, err := acTempFile("payload.json", "")
	if err != nil {
		return err
	}
	defer clean()
	if err := acExpectFailure(t, "DeleteHostedConfigurationVersion", "ResourceNotFoundException", acStatusNotFound,
		"get-hosted-configuration-version",
		"--application-id", appID, "--configuration-profile-id", profID, "--version-number", "1", outPath); err != nil {
		return err
	}
	list, err := acOut(t, "list-hosted-configuration-versions",
		"--application-id", appID, "--configuration-profile-id", profID)
	if err != nil {
		return fmt.Errorf("appconfig DeleteHostedConfigurationVersion: list after delete failed: %w", err)
	}
	if acFindByID(acItems(list), "VersionNumber", "1") != nil {
		return fmt.Errorf("appconfig DeleteHostedConfigurationVersion: version 1 still listed after delete")
	}
	return nil
}

func (g *appconfigGroup) GetHostedConfigurationVersionNotFound(_ context.Context, t *harness.TestContext) error {
	outPath, clean, err := acTempFile("payload.json", "")
	if err != nil {
		return err
	}
	defer clean()
	return acExpectFailure(t, "GetHostedConfigurationVersionNotFound", "ResourceNotFoundException", acStatusNotFound,
		"get-hosted-configuration-version",
		"--application-id", t.GetString("ac_hcv_app_id"),
		"--configuration-profile-id", t.GetString("ac_hcv_prof_id"),
		"--version-number", "9999", outPath)
}

// ─── appconfig-deployment-strategies ──────────────────────────────────────────
//
// Overcast does not emulate deployments, so these are expected to answer 501.
// They are here because the URIs are the thing under test: `/deploymentstrategies`
// is exactly the kind of collection route that answered the wrong service in
// issue #963, and a 501 with an AppConfig-shaped error body is the correct
// result while the operations are unimplemented.

func (g *appconfigGroup) teardownDeploymentStrategies(_ context.Context, t *harness.TestContext) error {
	if id := t.GetString("ac_ds_id"); id != "" {
		acRun(t, "delete-deployment-strategy", "--deployment-strategy-id", id) //nolint:errcheck
	}
	return nil
}

// acStrategyID returns the created strategy id, or a stand-in so the remaining
// URIs are still exercised when CreateDeploymentStrategy is unimplemented.
func acStrategyID(t *harness.TestContext) string {
	if id := t.GetString("ac_ds_id"); id != "" {
		return id
	}
	return acMissingID
}

func (g *appconfigGroup) CreateDeploymentStrategy(_ context.Context, t *harness.TestContext) error {
	name := acStrategyNamer.Name(t)
	out, err := acOut(t, "create-deployment-strategy",
		"--name", name,
		"--deployment-duration-in-minutes", "0",
		"--growth-factor", "100",
		"--replicate-to", "NONE",
	)
	if err != nil {
		return err
	}
	id := acString(out, "Id")
	if id == "" {
		return fmt.Errorf("appconfig CreateDeploymentStrategy: no Id in response: %v", out)
	}
	t.Set("ac_ds_id", id)
	if acString(out, "Name") != name {
		return fmt.Errorf("appconfig CreateDeploymentStrategy: expected Name %q, got %v", name, out)
	}
	got, err := acOut(t, "get-deployment-strategy", "--deployment-strategy-id", id)
	if err != nil {
		return fmt.Errorf("appconfig CreateDeploymentStrategy: get after create failed: %w", err)
	}
	if acString(got, "Name") != name {
		return fmt.Errorf("appconfig CreateDeploymentStrategy: get returned %v, want Name %q", got, name)
	}
	return nil
}

func (g *appconfigGroup) GetDeploymentStrategy(_ context.Context, t *harness.TestContext) error {
	out, err := acOut(t, "get-deployment-strategy", "--deployment-strategy-id", acStrategyID(t))
	if err != nil {
		return err
	}
	if acString(out, "Id") == "" {
		return fmt.Errorf("appconfig GetDeploymentStrategy: no Id in response: %v", out)
	}
	return nil
}

func (g *appconfigGroup) ListDeploymentStrategies(_ context.Context, t *harness.TestContext) error {
	out, err := acOut(t, "list-deployment-strategies")
	if err != nil {
		return err
	}
	if _, ok := out["Items"]; !ok {
		return fmt.Errorf("appconfig ListDeploymentStrategies: no Items member in the response: %v", out)
	}
	if id := t.GetString("ac_ds_id"); id != "" && acFindByID(acItems(out), "Id", id) == nil {
		return fmt.Errorf("appconfig ListDeploymentStrategies: strategy %q absent from the listing", id)
	}
	return nil
}

func (g *appconfigGroup) UpdateDeploymentStrategy(_ context.Context, t *harness.TestContext) error {
	const updated = "updated by compat"
	out, err := acOut(t, "update-deployment-strategy",
		"--deployment-strategy-id", acStrategyID(t), "--description", updated)
	if err != nil {
		return err
	}
	if acString(out, "Description") != updated {
		return fmt.Errorf("appconfig UpdateDeploymentStrategy: expected Description %q, got %v", updated, out)
	}
	return nil
}

func (g *appconfigGroup) DeleteDeploymentStrategy(_ context.Context, t *harness.TestContext) error {
	id := acStrategyID(t)
	if err := acRun(t, "delete-deployment-strategy", "--deployment-strategy-id", id); err != nil {
		return err
	}
	t.Set("ac_ds_id", "")
	return acExpectFailure(t, "DeleteDeploymentStrategy", "ResourceNotFoundException", acStatusNotFound,
		"get-deployment-strategy", "--deployment-strategy-id", id)
}

// ─── appconfig-extensions ─────────────────────────────────────────────────────
//
// Unimplemented too. `/extensions` and `/extensionassociations` are top-level
// collection URIs with no prerequisites, which makes them the closest analogue
// in this service to the routes issue #963 was about.

const acExtensionActions = `{"PRE_START_DEPLOYMENT":[{"Name":"compat","Uri":"arn:aws:sns:us-east-1:000000000000:compat"}]}`

func (g *appconfigGroup) teardownExtensions(_ context.Context, t *harness.TestContext) error {
	if id := t.GetString("ac_ext_assoc_id"); id != "" {
		acRun(t, "delete-extension-association", "--extension-association-id", id) //nolint:errcheck
	}
	if id := t.GetString("ac_ext_id"); id != "" {
		acRun(t, "delete-extension", "--extension-identifier", id) //nolint:errcheck
	}
	return nil
}

func acExtensionID(t *harness.TestContext) string {
	if id := t.GetString("ac_ext_id"); id != "" {
		return id
	}
	return acMissingID
}

func acExtensionAssociationID(t *harness.TestContext) string {
	if id := t.GetString("ac_ext_assoc_id"); id != "" {
		return id
	}
	return acMissingID
}

func (g *appconfigGroup) CreateExtension(_ context.Context, t *harness.TestContext) error {
	name := acExtNamer.Name(t)
	out, err := acOut(t, "create-extension", "--name", name, "--actions", acExtensionActions)
	if err != nil {
		return err
	}
	id := acString(out, "Id")
	if id == "" {
		return fmt.Errorf("appconfig CreateExtension: no Id in response: %v", out)
	}
	t.Set("ac_ext_id", id)
	if acString(out, "Name") != name {
		return fmt.Errorf("appconfig CreateExtension: expected Name %q, got %v", name, out)
	}
	return nil
}

func (g *appconfigGroup) GetExtension(_ context.Context, t *harness.TestContext) error {
	out, err := acOut(t, "get-extension", "--extension-identifier", acExtensionID(t))
	if err != nil {
		return err
	}
	if acString(out, "Id") == "" {
		return fmt.Errorf("appconfig GetExtension: no Id in response: %v", out)
	}
	return nil
}

func (g *appconfigGroup) ListExtensions(_ context.Context, t *harness.TestContext) error {
	out, err := acOut(t, "list-extensions")
	if err != nil {
		return err
	}
	if _, ok := out["Items"]; !ok {
		return fmt.Errorf("appconfig ListExtensions: no Items member in the response: %v", out)
	}
	if id := t.GetString("ac_ext_id"); id != "" && acFindByID(acItems(out), "Id", id) == nil {
		return fmt.Errorf("appconfig ListExtensions: extension %q absent from the listing", id)
	}
	return nil
}

func (g *appconfigGroup) UpdateExtension(_ context.Context, t *harness.TestContext) error {
	const updated = "updated by compat"
	out, err := acOut(t, "update-extension",
		"--extension-identifier", acExtensionID(t), "--description", updated)
	if err != nil {
		return err
	}
	if acString(out, "Description") != updated {
		return fmt.Errorf("appconfig UpdateExtension: expected Description %q, got %v", updated, out)
	}
	return nil
}

func (g *appconfigGroup) DeleteExtension(_ context.Context, t *harness.TestContext) error {
	id := acExtensionID(t)
	if err := acRun(t, "delete-extension", "--extension-identifier", id); err != nil {
		return err
	}
	t.Set("ac_ext_id", "")
	return acExpectFailure(t, "DeleteExtension", "ResourceNotFoundException", acStatusNotFound,
		"get-extension", "--extension-identifier", id)
}

func (g *appconfigGroup) CreateExtensionAssociation(_ context.Context, t *harness.TestContext) error {
	out, err := acOut(t, "create-extension-association",
		"--extension-identifier", acExtensionID(t),
		"--resource-identifier", acARN(t, "application/"+acMissingID))
	if err != nil {
		return err
	}
	id := acString(out, "Id")
	if id == "" {
		return fmt.Errorf("appconfig CreateExtensionAssociation: no Id in response: %v", out)
	}
	t.Set("ac_ext_assoc_id", id)
	return nil
}

func (g *appconfigGroup) GetExtensionAssociation(_ context.Context, t *harness.TestContext) error {
	out, err := acOut(t, "get-extension-association",
		"--extension-association-id", acExtensionAssociationID(t))
	if err != nil {
		return err
	}
	if acString(out, "Id") == "" {
		return fmt.Errorf("appconfig GetExtensionAssociation: no Id in response: %v", out)
	}
	return nil
}

func (g *appconfigGroup) ListExtensionAssociations(_ context.Context, t *harness.TestContext) error {
	out, err := acOut(t, "list-extension-associations")
	if err != nil {
		return err
	}
	if _, ok := out["Items"]; !ok {
		return fmt.Errorf("appconfig ListExtensionAssociations: no Items member in the response: %v", out)
	}
	return nil
}

func (g *appconfigGroup) UpdateExtensionAssociation(_ context.Context, t *harness.TestContext) error {
	out, err := acOut(t, "update-extension-association",
		"--extension-association-id", acExtensionAssociationID(t),
		"--parameters", `{"compat":"true"}`)
	if err != nil {
		return err
	}
	if acString(out, "Id") == "" {
		return fmt.Errorf("appconfig UpdateExtensionAssociation: no Id in response: %v", out)
	}
	return nil
}

func (g *appconfigGroup) DeleteExtensionAssociation(_ context.Context, t *harness.TestContext) error {
	id := acExtensionAssociationID(t)
	if err := acRun(t, "delete-extension-association", "--extension-association-id", id); err != nil {
		return err
	}
	t.Set("ac_ext_assoc_id", "")
	return acExpectFailure(t, "DeleteExtensionAssociation", "ResourceNotFoundException", acStatusNotFound,
		"get-extension-association", "--extension-association-id", id)
}

// ─── appconfig-experiments ────────────────────────────────────────────────────
//
// The experiment surface is unimplemented, and its definitions cannot be
// created, so only the three collection URIs are exercised. Each is expected to
// answer 501 rather than fall through to another service's route.

func (g *appconfigGroup) ListExperimentDefinitions(_ context.Context, t *harness.TestContext) error {
	out, err := acOut(t, "list-experiment-definitions", "--application-identifier", acMissingID)
	if err != nil {
		return err
	}
	if _, ok := out["Items"]; !ok {
		return fmt.Errorf("appconfig ListExperimentDefinitions: no Items member in the response: %v", out)
	}
	return nil
}

func (g *appconfigGroup) ListExperimentRuns(_ context.Context, t *harness.TestContext) error {
	out, err := acOut(t, "list-experiment-runs",
		"--application-identifier", acMissingID, "--experiment-definition-identifier", acMissingID)
	if err != nil {
		return err
	}
	if _, ok := out["Items"]; !ok {
		return fmt.Errorf("appconfig ListExperimentRuns: no Items member in the response: %v", out)
	}
	return nil
}

func (g *appconfigGroup) ListExperimentRunEvents(_ context.Context, t *harness.TestContext) error {
	out, err := acOut(t, "list-experiment-run-events",
		"--application-identifier", acMissingID,
		"--experiment-definition-identifier", acMissingID,
		"--run", "1")
	if err != nil {
		return err
	}
	if _, ok := out["Items"]; !ok {
		return fmt.Errorf("appconfig ListExperimentRunEvents: no Items member in the response: %v", out)
	}
	return nil
}
