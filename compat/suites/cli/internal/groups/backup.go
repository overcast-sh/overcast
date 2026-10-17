package groups

import (
	"context"
	"fmt"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// The Backup sub-group that keeps a native gets its own Namer so its vault never
// collides with a vault another Backup group (an authored scenario, running in
// parallel) creates.
var backupJobsNamer = harness.NewNamer("bkj")

// Backup returns the AWS Backup service group.
//
// Only backup-jobs has a native here. backup-vaults, backup-plans,
// backup-selections and backup-tags resolve through their authored scenarios
// (compat/model/authored/backup-{vaults,plans,selections,tags}.json).
//
// The unsigned-request coverage the vault and plan natives carried (#963: an
// unsigned request for a Backup collection fell through to S3's wildcard and
// came back HTTP 200 with a <ListBucketResult>) now lives in
// tests/integration/backup/unsigned_test.go (#2327).
func Backup() ServiceGroup {
	g := &backupCliGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"backup-jobs:StartBackupJob":                  g.StartBackupJob,
			"backup-jobs:ListBackupJobs":                  g.ListBackupJobs,
			"backup-jobs:DescribeBackupJob":               g.DescribeBackupJob,
			"backup-jobs:ListRecoveryPointsByBackupVault": g.ListRecoveryPointsByBackupVault,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"backup-jobs": g.setupJobs,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"backup-jobs": g.teardownJobs,
		},
	}
}

type backupCliGroup struct{}

// ─── Error-shape helpers ──────────────────────────────────────────────────────

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

// ─── backup-jobs ──────────────────────────────────────────────────────────────
//
// Backup jobs are not emulated: Backup implements a fraction of the 115
// operations AWS models, and the job operations are outside it. Those tests exist anyway —
// compat/AGENTS.md § Core principles 3, tests cover all services, not just
// implemented ones — and each records `unimplemented` against the 501 the
// emulator answers. That is the coverage metric, and it is what makes the day
// one of these is implemented visible in the results rather than invisible.
//
// They call **signed**. Overcast serves every
// service off one listener and picks the owner of an unclaimed path from the
// SigV4 credential scope (`addressesNonS3` in internal/router/router.go treats
// unsigned traffic as S3's), so an unsigned request for an unbound Backup path
// reaches S3's wildcard and comes back as an S3 XML 404 — a `fail`, not the
// `unimplemented` that is true. A real Backup client always signs.
//
// The vault and plan natives this file used to carry called unsigned,
// deliberately, because unsigned is where #963 hid. That guard now lives in
// tests/integration/backup/unsigned_test.go (#2327).
//
// The backup-jobs tests declare no `depends`. With nothing implemented, a chain
// would turn three honest `unimplemented` results into one `unimplemented` and
// two `dependency failed` skips, which reads as a cascade rather than as the gap
// it is. When the feature lands, wire the dependencies up then.
//
// Every assertion a working implementation would have to satisfy is already
// written below, so the tests measure the gap today and the behaviour later.

// backupMissingJobID stands in for an identifier StartBackupJob cannot yet mint.
const backupMissingJobID = "22222222-2222-2222-2222-222222222222"

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
