package groups

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// One Namer per Backup sub-group so the four groups, which run in parallel,
// never share a vault, plan, selection or job name.
var (
	backupVaultsNamer     = harness.NewNamer("bkv")
	backupPlansNamer      = harness.NewNamer("bkp")
	backupSelectionsNamer = harness.NewNamer("bks")
	backupJobsNamer       = harness.NewNamer("bkj")
)

// backupMissingPlanID is a well-formed BackupPlanId that was never created. The
// negative-path tests use it rather than the group's own plan so they do not
// depend on a delete having run first — a dependency that would turn one
// failure into a cascade of skips.
const backupMissingPlanID = "00000000-0000-0000-0000-000000000000"

// Backup returns the AWS Backup service group.
//
// The point of this group is the URI the AWS CLI picks out of the pinned model,
// not just the response body. AWS binds Backup's collections to a trailing
// slash — ListBackupVaults to GET /backup-vaults/, ListBackupPlans and
// CreateBackupPlan to /backup/plans/, GetBackupPlan to
// /backup/plans/{backupPlanId}/ — while the vault item operations and
// Update/DeleteBackupPlan carry no slash. Overcast matched only the slash-less
// spelling of the collections, so three operations answered 501 to a signed
// client and, unsigned, fell through to S3's wildcard and came back HTTP 200
// with a <ListBucketResult> a caller reads as an empty-but-successful list
// (#963, fixed in #966). Every List/Get test below therefore asserts that the
// resource it created is *in* the response, because "the call did not throw" is
// exactly what that bug looked like.
//
// backup-tags has no native here: it resolves through its authored scenario
// (compat/model/authored/backup-tags.json).
func Backup() ServiceGroup {
	g := &backupCliGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			// backup-vaults
			"backup-vaults:CreateBackupVault":              g.CreateBackupVault,
			"backup-vaults:DescribeBackupVault":            g.DescribeBackupVault,
			"backup-vaults:ListBackupVaults":               g.ListBackupVaults,
			"backup-vaults:ListBackupVaultsPaginated":      g.ListBackupVaultsPaginated,
			"backup-vaults:ListBackupVaultsByShared":       g.ListBackupVaultsByShared,
			"backup-vaults:DescribeBackupVaultNotFound":    g.DescribeBackupVaultNotFound,
			"backup-vaults:CreateBackupVaultAlreadyExists": g.CreateBackupVaultAlreadyExists,
			"backup-vaults:CreateBackupVaultInvalidName":   g.CreateBackupVaultInvalidName,
			"backup-vaults:DeleteBackupVault":              g.DeleteBackupVault,
			"backup-vaults:DeleteBackupVaultNotFound":      g.DeleteBackupVaultNotFound,
			// backup-plans
			"backup-plans:CreateBackupPlan":             g.CreateBackupPlan,
			"backup-plans:GetBackupPlan":                g.GetBackupPlan,
			"backup-plans:ListBackupPlans":              g.ListBackupPlans,
			"backup-plans:ListBackupPlansPaginated":     g.ListBackupPlansPaginated,
			"backup-plans:UpdateBackupPlan":             g.UpdateBackupPlan,
			"backup-plans:GetBackupPlanNotFound":        g.GetBackupPlanNotFound,
			"backup-plans:GetBackupPlanVersionNotFound": g.GetBackupPlanVersionNotFound,
			"backup-plans:UpdateBackupPlanNotFound":     g.UpdateBackupPlanNotFound,
			"backup-plans:DeleteBackupPlan":             g.DeleteBackupPlan,
			"backup-plans:DeleteBackupPlanNotFound":     g.DeleteBackupPlanNotFound,
			// backup-selections
			"backup-selections:CreateBackupSelection": g.CreateBackupSelection,
			"backup-selections:ListBackupSelections":  g.ListBackupSelections,
			"backup-selections:GetBackupSelection":    g.GetBackupSelection,
			"backup-selections:DeleteBackupSelection": g.DeleteBackupSelection,
			// backup-jobs
			"backup-jobs:StartBackupJob":                  g.StartBackupJob,
			"backup-jobs:ListBackupJobs":                  g.ListBackupJobs,
			"backup-jobs:DescribeBackupJob":               g.DescribeBackupJob,
			"backup-jobs:ListRecoveryPointsByBackupVault": g.ListRecoveryPointsByBackupVault,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"backup-vaults":     g.setupVaults,
			"backup-plans":      g.setupPlans,
			"backup-selections": g.setupSelections,
			"backup-jobs":       g.setupJobs,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"backup-vaults":     g.teardownVaults,
			"backup-plans":      g.teardownPlans,
			"backup-selections": g.teardownSelections,
			"backup-jobs":       g.teardownJobs,
		},
	}
}

type backupCliGroup struct{}

// ─── Error-shape helpers ──────────────────────────────────────────────────────

// expectBackupError runs an `aws backup` command that must fail and asserts the
// AWS error code *and* the HTTP status the model declares. No Backup error
// shape carries an httpError trait except ConflictException, so every client
// error in this service is a 400 — a 404 is wrong even when the error code
// reads correctly, and alpha.35 moved these off 404. The CLI prints the code
// and never the status, which is what awscli.RunStatus exists to recover.
func expectBackupError(t *harness.TestContext, op, wantCode string, wantStatus int, args ...string) error {
	status, err := awscli.RunStatus(t.Endpoint, t.Region, append([]string{"backup"}, args...)...)
	if err == nil {
		return fmt.Errorf("backup %s: expected %s with HTTP %d, but the call succeeded", op, wantCode, wantStatus)
	}
	if status != wantStatus {
		return fmt.Errorf("backup %s: answered HTTP %d, want %d (%s): %v", op, status, wantStatus, wantCode, err)
	}
	if !strings.Contains(err.Error(), wantCode) {
		return fmt.Errorf("backup %s: answered HTTP %d but not %s: %v", op, status, wantCode, err)
	}
	return nil
}

// backupString reads a string member, returning "" when it is absent or of
// another type.
func backupString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// backupList reads a modeled list member. The second return distinguishes "the
// member is an empty list" from "the member is not there at all", which is what
// a response served by another service looks like.
func backupList(m map[string]any, key string) ([]any, bool) {
	v, present := m[key]
	if !present {
		return nil, false
	}
	items, ok := v.([]any)
	return items, ok
}

// ─── backup-vaults ────────────────────────────────────────────────────────────

func (g *backupCliGroup) setupVaults(_ context.Context, _ *harness.TestContext) error { return nil }

func (g *backupCliGroup) teardownVaults(_ context.Context, t *harness.TestContext) error {
	// Reverse creation order: the paginated test's second vault, then the one
	// the group is built around.
	for _, key := range []string{"vault_name_2", "vault_name"} {
		if name := t.GetString(key); name != "" {
			awscli.Run(t.Endpoint, t.Region, "backup", "delete-backup-vault", "--backup-vault-name", name) //nolint:errcheck
		}
	}
	return nil
}

func (g *backupCliGroup) CreateBackupVault(_ context.Context, t *harness.TestContext) error {
	name := backupVaultsNamer.Name(t)
	t.Set("vault_name", name)
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"backup", "create-backup-vault", "--backup-vault-name", name)
	if err != nil {
		return err
	}
	if got := backupString(out, "BackupVaultName"); got != name {
		return fmt.Errorf("backup CreateBackupVault: BackupVaultName = %q, want %q", got, name)
	}
	arn := backupString(out, "BackupVaultArn")
	if !strings.HasSuffix(arn, ":backup-vault:"+name) {
		return fmt.Errorf("backup CreateBackupVault: BackupVaultArn = %q, want it to end in :backup-vault:%s", arn, name)
	}
	if out["CreationDate"] == nil {
		return fmt.Errorf("backup CreateBackupVault: CreationDate is absent")
	}
	return nil
}

func (g *backupCliGroup) DescribeBackupVault(_ context.Context, t *harness.TestContext) error {
	name := t.GetString("vault_name")
	if name == "" {
		return fmt.Errorf("backup DescribeBackupVault: no vault_name from CreateBackupVault")
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"backup", "describe-backup-vault", "--backup-vault-name", name)
	if err != nil {
		return err
	}
	if got := backupString(out, "BackupVaultName"); got != name {
		return fmt.Errorf("backup DescribeBackupVault: BackupVaultName = %q, want %q", got, name)
	}
	if got := backupString(out, "BackupVaultArn"); !strings.HasSuffix(got, ":backup-vault:"+name) {
		return fmt.Errorf("backup DescribeBackupVault: BackupVaultArn = %q, want it to end in :backup-vault:%s", got, name)
	}
	// A vault created through this API is a standard, available vault, and no
	// backup job runs behind the control plane, so it holds no recovery points.
	if got := backupString(out, "VaultType"); got != "BACKUP_VAULT" {
		return fmt.Errorf("backup DescribeBackupVault: VaultType = %q, want BACKUP_VAULT", got)
	}
	if got := backupString(out, "VaultState"); got != "AVAILABLE" {
		return fmt.Errorf("backup DescribeBackupVault: VaultState = %q, want AVAILABLE", got)
	}
	if got, ok := out["NumberOfRecoveryPoints"].(float64); !ok || got != 0 {
		return fmt.Errorf("backup DescribeBackupVault: NumberOfRecoveryPoints = %#v, want 0", out["NumberOfRecoveryPoints"])
	}
	if got, ok := out["Locked"].(bool); !ok || got {
		return fmt.Errorf("backup DescribeBackupVault: Locked = %#v, want false", out["Locked"])
	}
	return nil
}

func (g *backupCliGroup) ListBackupVaults(_ context.Context, t *harness.TestContext) error {
	name := t.GetString("vault_name")
	if name == "" {
		return fmt.Errorf("backup ListBackupVaults: no vault_name from CreateBackupVault")
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "backup", "list-backup-vaults")
	if err != nil {
		return err
	}
	names, err := backupVaultNames(out, "ListBackupVaults")
	if err != nil {
		return err
	}
	if !slices.Contains(names, name) {
		return fmt.Errorf("backup ListBackupVaults: %q is not in %v", name, names)
	}
	return nil
}

// ListBackupVaultsPaginated drives the collection a page at a time, which is
// the only way the CLI exercises the NextToken round trip: --page-size sets
// maxResults and the CLI follows the token it gets back.
func (g *backupCliGroup) ListBackupVaultsPaginated(_ context.Context, t *harness.TestContext) error {
	first := t.GetString("vault_name")
	if first == "" {
		return fmt.Errorf("backup ListBackupVaultsPaginated: no vault_name from CreateBackupVault")
	}
	second := backupVaultsNamer.Suffixed(t, "-2")
	t.Set("vault_name_2", second)
	if err := awscli.Run(t.Endpoint, t.Region,
		"backup", "create-backup-vault", "--backup-vault-name", second); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"backup", "list-backup-vaults", "--page-size", "1")
	if err != nil {
		return err
	}
	names, err := backupVaultNames(out, "ListBackupVaultsPaginated")
	if err != nil {
		return err
	}
	for _, want := range []string{first, second} {
		if !slices.Contains(names, want) {
			return fmt.Errorf("backup ListBackupVaultsPaginated: %q is missing from the paged listing %v — NextToken did not carry the caller past page one", want, names)
		}
	}
	return nil
}

// ListBackupVaultsByShared pins the ByShared filter. Overcast creates only
// unshared vaults, so the filter must answer with a list that excludes them
// rather than ignoring the parameter.
func (g *backupCliGroup) ListBackupVaultsByShared(_ context.Context, t *harness.TestContext) error {
	name := t.GetString("vault_name")
	if name == "" {
		return fmt.Errorf("backup ListBackupVaultsByShared: no vault_name from CreateBackupVault")
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"backup", "list-backup-vaults", "--by-shared")
	if err != nil {
		return err
	}
	names, err := backupVaultNames(out, "ListBackupVaultsByShared")
	if err != nil {
		return err
	}
	if slices.Contains(names, name) {
		return fmt.Errorf("backup ListBackupVaultsByShared: --by-shared returned the unshared vault %q", name)
	}
	return nil
}

func (g *backupCliGroup) DescribeBackupVaultNotFound(_ context.Context, t *harness.TestContext) error {
	return expectBackupError(t, "DescribeBackupVaultNotFound", "ResourceNotFoundException", 400,
		"describe-backup-vault", "--backup-vault-name", backupVaultsNamer.Suffixed(t, "-absent"))
}

func (g *backupCliGroup) CreateBackupVaultAlreadyExists(_ context.Context, t *harness.TestContext) error {
	name := t.GetString("vault_name")
	if name == "" {
		return fmt.Errorf("backup CreateBackupVaultAlreadyExists: no vault_name from CreateBackupVault")
	}
	return expectBackupError(t, "CreateBackupVaultAlreadyExists", "AlreadyExistsException", 400,
		"create-backup-vault", "--backup-vault-name", name)
}

// CreateBackupVaultInvalidName sends a name outside BackupVaultName's modeled
// pattern (^[a-zA-Z0-9\-\_]{2,50}$). botocore does not enforce pattern traits,
// so the request reaches the server and the server has to reject it.
func (g *backupCliGroup) CreateBackupVaultInvalidName(_ context.Context, t *harness.TestContext) error {
	return expectBackupError(t, "CreateBackupVaultInvalidName", "InvalidParameterValueException", 400,
		"create-backup-vault", "--backup-vault-name", "oc.invalid.vault.name")
}

func (g *backupCliGroup) DeleteBackupVault(_ context.Context, t *harness.TestContext) error {
	name := t.GetString("vault_name")
	if name == "" {
		return fmt.Errorf("backup DeleteBackupVault: no vault_name from CreateBackupVault")
	}
	if err := awscli.Run(t.Endpoint, t.Region,
		"backup", "delete-backup-vault", "--backup-vault-name", name); err != nil {
		return err
	}
	// DeleteBackupVault's modeled output is Unit, so there is nothing in the
	// response to check: the delete is proved by the vault no longer being
	// describable.
	return expectBackupError(t, "DescribeBackupVault after DeleteBackupVault", "ResourceNotFoundException", 400,
		"describe-backup-vault", "--backup-vault-name", name)
}

func (g *backupCliGroup) DeleteBackupVaultNotFound(_ context.Context, t *harness.TestContext) error {
	return expectBackupError(t, "DeleteBackupVaultNotFound", "ResourceNotFoundException", 400,
		"delete-backup-vault", "--backup-vault-name", backupVaultsNamer.Suffixed(t, "-absent"))
}

// backupVaultNames pulls the vault names out of a ListBackupVaults response.
// The missing-member case is reported separately from the empty-list case:
// a response with no BackupVaultList at all is what a caller gets when the
// collection URI reached a different service instead of Backup.
func backupVaultNames(out map[string]any, op string) ([]string, error) {
	items, ok := backupList(out, "BackupVaultList")
	if !ok {
		return nil, fmt.Errorf("backup %s: the response carries no BackupVaultList — GET /backup-vaults/ was not answered by Backup: %#v", op, out)
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		vault, _ := item.(map[string]any)
		names = append(names, backupString(vault, "BackupVaultName"))
	}
	return names, nil
}

// ─── backup-plans ─────────────────────────────────────────────────────────────

// setupPlans creates the vault the group's rules target. AWS requires
// TargetBackupVaultName to name a real vault, so the group owns one rather than
// borrowing the vaults group's — the two run in parallel.
func (g *backupCliGroup) setupPlans(_ context.Context, t *harness.TestContext) error {
	vault := backupPlansNamer.Suffixed(t, "-vault")
	t.Set("plan_vault", vault)
	return awscli.Run(t.Endpoint, t.Region,
		"backup", "create-backup-vault", "--backup-vault-name", vault)
}

func (g *backupCliGroup) teardownPlans(_ context.Context, t *harness.TestContext) error {
	// Reverse creation order: both plans, then the vault their rules target.
	for _, key := range []string{"plan_id_2", "plan_id"} {
		if id := t.GetString(key); id != "" {
			awscli.Run(t.Endpoint, t.Region, "backup", "delete-backup-plan", "--backup-plan-id", id) //nolint:errcheck
		}
	}
	if vault := t.GetString("plan_vault"); vault != "" {
		awscli.Run(t.Endpoint, t.Region, "backup", "delete-backup-vault", "--backup-vault-name", vault) //nolint:errcheck
	}
	return nil
}

// backupPlanDocument renders a BackupPlanInput with one rule.
func backupPlanDocument(name, rule, vault string) string {
	return fmt.Sprintf(
		`{"BackupPlanName":%q,"Rules":[{"RuleName":%q,"TargetBackupVaultName":%q,"ScheduleExpression":"cron(0 5 ? * * *)"}]}`,
		name, rule, vault)
}

func (g *backupCliGroup) CreateBackupPlan(_ context.Context, t *harness.TestContext) error {
	vault := t.GetString("plan_vault")
	if vault == "" {
		return fmt.Errorf("backup CreateBackupPlan: no plan_vault from setup")
	}
	name := backupPlansNamer.Name(t)
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"backup", "create-backup-plan", "--backup-plan", backupPlanDocument(name, "daily", vault))
	if err != nil {
		return err
	}
	id := backupString(out, "BackupPlanId")
	if id == "" {
		return fmt.Errorf("backup CreateBackupPlan: BackupPlanId is empty: %#v", out)
	}
	t.Set("plan_id", id)
	t.Set("plan_name", name)
	if arn := backupString(out, "BackupPlanArn"); !strings.HasSuffix(arn, ":backup-plan:"+id) {
		return fmt.Errorf("backup CreateBackupPlan: BackupPlanArn = %q, want it to end in :backup-plan:%s", arn, id)
	}
	version := backupString(out, "VersionId")
	if version == "" {
		return fmt.Errorf("backup CreateBackupPlan: VersionId is empty: %#v", out)
	}
	t.Set("plan_version", version)
	if out["CreationDate"] == nil {
		return fmt.Errorf("backup CreateBackupPlan: CreationDate is absent")
	}
	return nil
}

// GetBackupPlan addresses GET /backup/plans/{backupPlanId}/ — the trailing
// slash is in AWS's own model, and it is the only spelling the CLI will ever
// send.
func (g *backupCliGroup) GetBackupPlan(_ context.Context, t *harness.TestContext) error {
	id := t.GetString("plan_id")
	name := t.GetString("plan_name")
	vault := t.GetString("plan_vault")
	if id == "" || name == "" {
		return fmt.Errorf("backup GetBackupPlan: no plan_id/plan_name from CreateBackupPlan")
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"backup", "get-backup-plan", "--backup-plan-id", id)
	if err != nil {
		return err
	}
	if got := backupString(out, "BackupPlanId"); got != id {
		return fmt.Errorf("backup GetBackupPlan: BackupPlanId = %q, want %q", got, id)
	}
	plan, ok := out["BackupPlan"].(map[string]any)
	if !ok {
		return fmt.Errorf("backup GetBackupPlan: the response carries no BackupPlan: %#v", out)
	}
	if got := backupString(plan, "BackupPlanName"); got != name {
		return fmt.Errorf("backup GetBackupPlan: BackupPlanName = %q, want %q", got, name)
	}
	rules, ok := backupList(plan, "Rules")
	if !ok || len(rules) != 1 {
		return fmt.Errorf("backup GetBackupPlan: Rules = %#v, want the one rule that was created", plan["Rules"])
	}
	rule, _ := rules[0].(map[string]any)
	if got := backupString(rule, "RuleName"); got != "daily" {
		return fmt.Errorf("backup GetBackupPlan: Rules[0].RuleName = %q, want daily", got)
	}
	if got := backupString(rule, "TargetBackupVaultName"); got != vault {
		return fmt.Errorf("backup GetBackupPlan: Rules[0].TargetBackupVaultName = %q, want %q", got, vault)
	}
	if got := backupString(rule, "ScheduleExpression"); got != "cron(0 5 ? * * *)" {
		return fmt.Errorf("backup GetBackupPlan: Rules[0].ScheduleExpression = %q, want the expression the plan was created with", got)
	}
	return nil
}

func (g *backupCliGroup) ListBackupPlans(_ context.Context, t *harness.TestContext) error {
	id := t.GetString("plan_id")
	name := t.GetString("plan_name")
	if id == "" {
		return fmt.Errorf("backup ListBackupPlans: no plan_id from CreateBackupPlan")
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "backup", "list-backup-plans")
	if err != nil {
		return err
	}
	plans, err := backupPlanMembers(out, "ListBackupPlans")
	if err != nil {
		return err
	}
	member, ok := plans[id]
	if !ok {
		return fmt.Errorf("backup ListBackupPlans: plan %s is not in the listing (%d plans)", id, len(plans))
	}
	if got := backupString(member, "BackupPlanName"); got != name {
		return fmt.Errorf("backup ListBackupPlans: BackupPlanName = %q, want %q", got, name)
	}
	return nil
}

// ListBackupPlansPaginated adds a second plan and walks the collection one page
// at a time, so a NextToken that does not round-trip loses a plan.
func (g *backupCliGroup) ListBackupPlansPaginated(_ context.Context, t *harness.TestContext) error {
	first := t.GetString("plan_id")
	vault := t.GetString("plan_vault")
	if first == "" {
		return fmt.Errorf("backup ListBackupPlansPaginated: no plan_id from CreateBackupPlan")
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "backup", "create-backup-plan",
		"--backup-plan", backupPlanDocument(backupPlansNamer.Suffixed(t, "-2"), "weekly", vault))
	if err != nil {
		return err
	}
	second := backupString(out, "BackupPlanId")
	if second == "" {
		return fmt.Errorf("backup ListBackupPlansPaginated: the second CreateBackupPlan returned no BackupPlanId")
	}
	t.Set("plan_id_2", second)

	listed, err := awscli.RunOutput(t.Endpoint, t.Region,
		"backup", "list-backup-plans", "--page-size", "1")
	if err != nil {
		return err
	}
	plans, err := backupPlanMembers(listed, "ListBackupPlansPaginated")
	if err != nil {
		return err
	}
	for _, want := range []string{first, second} {
		if _, ok := plans[want]; !ok {
			return fmt.Errorf("backup ListBackupPlansPaginated: plan %s is missing from the paged listing (%d plans) — NextToken did not carry the caller past page one", want, len(plans))
		}
	}
	return nil
}

// UpdateBackupPlan verifies itself through ListBackupPlans rather than
// GetBackupPlan: the listing carries both BackupPlanName and VersionId, and
// checking the mutation here means a broken update fails at UpdateBackupPlan
// instead of surfacing two tests later.
func (g *backupCliGroup) UpdateBackupPlan(_ context.Context, t *harness.TestContext) error {
	id := t.GetString("plan_id")
	vault := t.GetString("plan_vault")
	before := t.GetString("plan_version")
	if id == "" || before == "" {
		return fmt.Errorf("backup UpdateBackupPlan: no plan_id/plan_version from CreateBackupPlan")
	}
	updated := t.GetString("plan_name") + "-updated"
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "backup", "update-backup-plan",
		"--backup-plan-id", id,
		"--backup-plan", backupPlanDocument(updated, "daily", vault))
	if err != nil {
		return err
	}
	if got := backupString(out, "BackupPlanId"); got != id {
		return fmt.Errorf("backup UpdateBackupPlan: BackupPlanId = %q, want %q", got, id)
	}
	after := backupString(out, "VersionId")
	if after == "" || after == before {
		return fmt.Errorf("backup UpdateBackupPlan: VersionId = %q, want a new version (it was %q before the update)", after, before)
	}
	if out["CreationDate"] == nil {
		return fmt.Errorf("backup UpdateBackupPlan: CreationDate is absent from the modeled output")
	}

	listed, err := awscli.RunOutput(t.Endpoint, t.Region, "backup", "list-backup-plans")
	if err != nil {
		return err
	}
	plans, err := backupPlanMembers(listed, "UpdateBackupPlan")
	if err != nil {
		return err
	}
	member, ok := plans[id]
	if !ok {
		return fmt.Errorf("backup UpdateBackupPlan: plan %s vanished from the listing after the update", id)
	}
	if got := backupString(member, "BackupPlanName"); got != updated {
		return fmt.Errorf("backup UpdateBackupPlan: BackupPlanName = %q after the update, want %q", got, updated)
	}
	if got := backupString(member, "VersionId"); got != after {
		return fmt.Errorf("backup UpdateBackupPlan: the listing reports VersionId %q, but the update returned %q", got, after)
	}
	t.Set("plan_name", updated)
	t.Set("plan_version", after)
	return nil
}

func (g *backupCliGroup) GetBackupPlanNotFound(_ context.Context, t *harness.TestContext) error {
	return expectBackupError(t, "GetBackupPlanNotFound", "ResourceNotFoundException", 400,
		"get-backup-plan", "--backup-plan-id", backupMissingPlanID)
}

// GetBackupPlanVersionNotFound asks for a version of a plan that does exist.
// Overcast keeps only the current version, so any other VersionId is not found
// — the failure mode to avoid is answering with the current version wearing the
// requested version's name.
func (g *backupCliGroup) GetBackupPlanVersionNotFound(_ context.Context, t *harness.TestContext) error {
	id := t.GetString("plan_id")
	if id == "" {
		return fmt.Errorf("backup GetBackupPlanVersionNotFound: no plan_id from CreateBackupPlan")
	}
	return expectBackupError(t, "GetBackupPlanVersionNotFound", "ResourceNotFoundException", 400,
		"get-backup-plan", "--backup-plan-id", id, "--version-id", "no-such-version")
}

func (g *backupCliGroup) UpdateBackupPlanNotFound(_ context.Context, t *harness.TestContext) error {
	vault := t.GetString("plan_vault")
	return expectBackupError(t, "UpdateBackupPlanNotFound", "ResourceNotFoundException", 400,
		"update-backup-plan",
		"--backup-plan-id", backupMissingPlanID,
		"--backup-plan", backupPlanDocument("oc-absent-plan", "daily", vault))
}

func (g *backupCliGroup) DeleteBackupPlan(_ context.Context, t *harness.TestContext) error {
	id := t.GetString("plan_id")
	if id == "" {
		return fmt.Errorf("backup DeleteBackupPlan: no plan_id from CreateBackupPlan")
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"backup", "delete-backup-plan", "--backup-plan-id", id)
	if err != nil {
		return err
	}
	if got := backupString(out, "BackupPlanId"); got != id {
		return fmt.Errorf("backup DeleteBackupPlan: BackupPlanId = %q, want %q", got, id)
	}
	if out["DeletionDate"] == nil {
		return fmt.Errorf("backup DeleteBackupPlan: DeletionDate is absent from the modeled output")
	}
	if backupString(out, "VersionId") == "" {
		return fmt.Errorf("backup DeleteBackupPlan: VersionId is absent from the modeled output")
	}

	listed, err := awscli.RunOutput(t.Endpoint, t.Region, "backup", "list-backup-plans")
	if err != nil {
		return err
	}
	plans, err := backupPlanMembers(listed, "DeleteBackupPlan")
	if err != nil {
		return err
	}
	if _, ok := plans[id]; ok {
		return fmt.Errorf("backup DeleteBackupPlan: plan %s is still in the listing after the delete", id)
	}
	return nil
}

func (g *backupCliGroup) DeleteBackupPlanNotFound(_ context.Context, t *harness.TestContext) error {
	return expectBackupError(t, "DeleteBackupPlanNotFound", "ResourceNotFoundException", 400,
		"delete-backup-plan", "--backup-plan-id", backupMissingPlanID)
}

// backupPlanMembers indexes a ListBackupPlans response by BackupPlanId. As with
// backupVaultNames, an absent BackupPlansList is reported as such: it is the
// shape of a response that came from somewhere other than Backup.
func backupPlanMembers(out map[string]any, op string) (map[string]map[string]any, error) {
	items, ok := backupList(out, "BackupPlansList")
	if !ok {
		return nil, fmt.Errorf("backup %s: the response carries no BackupPlansList — GET /backup/plans/ was not answered by Backup: %#v", op, out)
	}
	plans := make(map[string]map[string]any, len(items))
	for _, item := range items {
		member, _ := item.(map[string]any)
		plans[backupString(member, "BackupPlanId")] = member
	}
	return plans, nil
}

// ─── backup-selections and backup-jobs ────────────────────────────────────────
//
// Neither feature is emulated: Backup implements nine of the 115 operations AWS
// models, and everything below is outside that nine. The tests exist anyway —
// compat/AGENTS.md § Core principles 3, tests cover all services, not just
// implemented ones — and each records `unimplemented` against the 501 the
// emulator answers. That is the coverage metric, and it is what makes the day
// one of these is implemented visible in the results rather than invisible.
//
// They call **signed**, unlike the rest of this file. Overcast serves every
// service off one listener and picks the owner of an unclaimed path from the
// SigV4 credential scope (`addressesNonS3` in internal/router/router.go treats
// unsigned traffic as S3's), so an unsigned request for an unbound Backup path
// reaches S3's wildcard and comes back as an S3 XML 404 — a `fail`, not the
// `unimplemented` that is true. A real Backup client always signs.
//
// The happy-path groups above deliberately stay unsigned: unsigned is where
// #963 hid, and a signed call would have turned that fault into a tidy 501
// rather than the silent 200 it actually was.
//
// No test here declares `depends`. With nothing implemented, a chain would turn
// three honest `unimplemented` results into one `unimplemented` and two
// `dependency failed` skips, which reads as a cascade rather than as the gap it
// is. When the feature lands, wire the dependencies up then.
//
// Every assertion a working implementation would have to satisfy is already
// written below, so the tests measure the gap today and the behaviour later.

// backupMissingSelectionID and backupMissingJobID stand in for identifiers the
// create operations cannot yet mint.
const (
	backupMissingSelectionID = "11111111-1111-1111-1111-111111111111"
	backupMissingJobID       = "22222222-2222-2222-2222-222222222222"
)

// setupSelections creates the plan a selection hangs off, and the vault its
// rule targets. A selection is a child of a plan in AWS, so a test that did not
// have one would not be exercising the modeled shape.
func (g *backupCliGroup) setupSelections(_ context.Context, t *harness.TestContext) error {
	vault := backupSelectionsNamer.Suffixed(t, "-vault")
	t.Set("selection_vault", vault)
	if err := awscli.Run(t.Endpoint, t.Region,
		"backup", "create-backup-vault", "--backup-vault-name", vault); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "backup", "create-backup-plan",
		"--backup-plan", backupPlanDocument(backupSelectionsNamer.Name(t), "daily", vault))
	if err != nil {
		return err
	}
	id := backupString(out, "BackupPlanId")
	if id == "" {
		return fmt.Errorf("backup selections setup: CreateBackupPlan returned no BackupPlanId")
	}
	t.Set("selection_plan_id", id)
	return nil
}

func (g *backupCliGroup) teardownSelections(_ context.Context, t *harness.TestContext) error {
	// Reverse creation order: the selection (if one was ever minted), then the
	// plan, then the vault.
	planID := t.GetString("selection_plan_id")
	if id := t.GetString("selection_id"); id != "" && planID != "" {
		awscli.RunSigned(t.Endpoint, t.Region, "backup", "delete-backup-selection", //nolint:errcheck
			"--backup-plan-id", planID, "--selection-id", id)
	}
	if planID != "" {
		awscli.Run(t.Endpoint, t.Region, "backup", "delete-backup-plan", "--backup-plan-id", planID) //nolint:errcheck
	}
	if vault := t.GetString("selection_vault"); vault != "" {
		awscli.Run(t.Endpoint, t.Region, "backup", "delete-backup-vault", "--backup-vault-name", vault) //nolint:errcheck
	}
	return nil
}

// backupSelectionDocument renders a BackupSelection naming every resource, the
// broadest selection AWS accepts.
func backupSelectionDocument(name string) string {
	return fmt.Sprintf(
		`{"SelectionName":%q,"IamRoleArn":"arn:aws:iam::000000000000:role/service-role/AWSBackupDefaultServiceRole","Resources":["*"]}`,
		name)
}

func (g *backupCliGroup) CreateBackupSelection(_ context.Context, t *harness.TestContext) error {
	planID := t.GetString("selection_plan_id")
	if planID == "" {
		return fmt.Errorf("backup CreateBackupSelection: no selection_plan_id from setup")
	}
	name := backupSelectionsNamer.Suffixed(t, "-sel")
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "backup", "create-backup-selection",
		"--backup-plan-id", planID,
		"--backup-selection", backupSelectionDocument(name))
	if err != nil {
		return err
	}
	id := backupString(out, "SelectionId")
	if id == "" {
		return fmt.Errorf("backup CreateBackupSelection: SelectionId is empty: %#v", out)
	}
	t.Set("selection_id", id)
	if got := backupString(out, "BackupPlanId"); got != planID {
		return fmt.Errorf("backup CreateBackupSelection: BackupPlanId = %q, want %q", got, planID)
	}
	return nil
}

func (g *backupCliGroup) ListBackupSelections(_ context.Context, t *harness.TestContext) error {
	planID := t.GetString("selection_plan_id")
	if planID == "" {
		return fmt.Errorf("backup ListBackupSelections: no selection_plan_id from setup")
	}
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region,
		"backup", "list-backup-selections", "--backup-plan-id", planID)
	if err != nil {
		return err
	}
	if _, ok := backupList(out, "BackupSelectionsList"); !ok {
		return fmt.Errorf("backup ListBackupSelections: the response carries no BackupSelectionsList: %#v", out)
	}
	return nil
}

func (g *backupCliGroup) GetBackupSelection(_ context.Context, t *harness.TestContext) error {
	planID := t.GetString("selection_plan_id")
	if planID == "" {
		return fmt.Errorf("backup GetBackupSelection: no selection_plan_id from setup")
	}
	id := t.GetString("selection_id")
	if id == "" {
		// CreateBackupSelection could not mint one, so ask for an id that does
		// not exist: the modeled answer is ResourceNotFoundException, and the
		// point of the call is that Backup answers it at all.
		id = backupMissingSelectionID
	}
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "backup", "get-backup-selection",
		"--backup-plan-id", planID, "--selection-id", id)
	if err != nil {
		return err
	}
	if got := backupString(out, "SelectionId"); got != id {
		return fmt.Errorf("backup GetBackupSelection: SelectionId = %q, want %q", got, id)
	}
	if out["BackupSelection"] == nil {
		return fmt.Errorf("backup GetBackupSelection: the response carries no BackupSelection: %#v", out)
	}
	return nil
}

func (g *backupCliGroup) DeleteBackupSelection(_ context.Context, t *harness.TestContext) error {
	planID := t.GetString("selection_plan_id")
	if planID == "" {
		return fmt.Errorf("backup DeleteBackupSelection: no selection_plan_id from setup")
	}
	id := t.GetString("selection_id")
	if id == "" {
		id = backupMissingSelectionID
	}
	if err := awscli.RunSigned(t.Endpoint, t.Region, "backup", "delete-backup-selection",
		"--backup-plan-id", planID, "--selection-id", id); err != nil {
		return err
	}
	t.Set("selection_id", "")
	// DeleteBackupSelection's modeled output is Unit, so the delete is proved
	// by the selection no longer being gettable.
	return expectBackupError(t, "GetBackupSelection after DeleteBackupSelection", "ResourceNotFoundException", 400,
		"get-backup-selection", "--backup-plan-id", planID, "--selection-id", id)
}

// setupJobs creates the vault a backup job would write its recovery points to.
func (g *backupCliGroup) setupJobs(_ context.Context, t *harness.TestContext) error {
	vault := backupJobsNamer.Suffixed(t, "-vault")
	t.Set("job_vault", vault)
	return awscli.Run(t.Endpoint, t.Region,
		"backup", "create-backup-vault", "--backup-vault-name", vault)
}

func (g *backupCliGroup) teardownJobs(_ context.Context, t *harness.TestContext) error {
	if vault := t.GetString("job_vault"); vault != "" {
		awscli.Run(t.Endpoint, t.Region, "backup", "delete-backup-vault", "--backup-vault-name", vault) //nolint:errcheck
	}
	return nil
}

func (g *backupCliGroup) StartBackupJob(_ context.Context, t *harness.TestContext) error {
	vault := t.GetString("job_vault")
	if vault == "" {
		return fmt.Errorf("backup StartBackupJob: no job_vault from setup")
	}
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "backup", "start-backup-job",
		"--backup-vault-name", vault,
		"--resource-arn", "arn:aws:dynamodb:"+t.Region+":000000000000:table/"+backupJobsNamer.Suffixed(t, "-table"),
		"--iam-role-arn", "arn:aws:iam::000000000000:role/service-role/AWSBackupDefaultServiceRole",
	)
	if err != nil {
		return err
	}
	id := backupString(out, "BackupJobId")
	if id == "" {
		return fmt.Errorf("backup StartBackupJob: BackupJobId is empty: %#v", out)
	}
	t.Set("job_id", id)
	return nil
}

func (g *backupCliGroup) ListBackupJobs(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "backup", "list-backup-jobs")
	if err != nil {
		return err
	}
	if _, ok := backupList(out, "BackupJobs"); !ok {
		return fmt.Errorf("backup ListBackupJobs: the response carries no BackupJobs: %#v", out)
	}
	return nil
}

func (g *backupCliGroup) DescribeBackupJob(_ context.Context, t *harness.TestContext) error {
	id := t.GetString("job_id")
	if id == "" {
		// StartBackupJob could not mint one — see the note above this section.
		id = backupMissingJobID
	}
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region,
		"backup", "describe-backup-job", "--backup-job-id", id)
	if err != nil {
		return err
	}
	if got := backupString(out, "BackupJobId"); got != id {
		return fmt.Errorf("backup DescribeBackupJob: BackupJobId = %q, want %q", got, id)
	}
	return nil
}

func (g *backupCliGroup) ListRecoveryPointsByBackupVault(_ context.Context, t *harness.TestContext) error {
	vault := t.GetString("job_vault")
	if vault == "" {
		return fmt.Errorf("backup ListRecoveryPointsByBackupVault: no job_vault from setup")
	}
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region,
		"backup", "list-recovery-points-by-backup-vault", "--backup-vault-name", vault)
	if err != nil {
		return err
	}
	if _, ok := backupList(out, "RecoveryPoints"); !ok {
		return fmt.Errorf("backup ListRecoveryPointsByBackupVault: the response carries no RecoveryPoints: %#v", out)
	}
	return nil
}
