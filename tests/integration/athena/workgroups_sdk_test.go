package athena_test

// workgroups_sdk_test.go — workgroups and engine versions through the AWS SDK
// for Go v2: the built-in primary workgroup, duplicate creates, UpdateWorkGroup
// with ConfigurationUpdates, ListWorkGroups' summary shape, and the delete
// rules.

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestGetWorkGroup_primaryExistsUntouched(t *testing.T) {
	// Given: a fresh server
	c := athenaClient(t, helpers.NewTestServer(t))

	// When: the primary workgroup is read
	out := must[*athena.GetWorkGroupOutput](t, "GetWorkGroup")(c.GetWorkGroup(context.Background(), &athena.GetWorkGroupInput{WorkGroup: aws.String("primary")}))

	// Then: it exists, enabled, on the automatically chosen engine
	wg := out.WorkGroup
	if aws.ToString(wg.Name) != "primary" || wg.State != types.WorkGroupStateEnabled || wg.CreationTime == nil {
		t.Fatalf("primary = %+v", wg)
	}
	ev := wg.Configuration.EngineVersion
	if aws.ToString(ev.SelectedEngineVersion) != "AUTO" || aws.ToString(ev.EffectiveEngineVersion) != "Athena engine version 3" {
		t.Fatalf("primary engine = %+v", ev)
	}
	if aws.ToBool(wg.Configuration.EnforceWorkGroupConfiguration) {
		t.Fatal("primary enforces its configuration")
	}
}

func TestCreateWorkGroup_existingName(t *testing.T) {
	// Given: a workgroup
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))
	must[*athena.CreateWorkGroupOutput](t, "CreateWorkGroup")(c.CreateWorkGroup(ctx, &athena.CreateWorkGroupInput{Name: aws.String("analysts")}))

	// When: it, and primary, are created again
	_, dup := c.CreateWorkGroup(ctx, &athena.CreateWorkGroupInput{Name: aws.String("analysts"), Description: aws.String("second")})
	_, primary := c.CreateWorkGroup(ctx, &athena.CreateWorkGroupInput{Name: aws.String("primary")})

	// Then: both fail, and the first definition is untouched
	wantAPIError(t, "duplicate CreateWorkGroup", dup, "InvalidRequestException")
	wantAPIError(t, "CreateWorkGroup primary", primary, "InvalidRequestException")
	got := must[*athena.GetWorkGroupOutput](t, "GetWorkGroup")(c.GetWorkGroup(ctx, &athena.GetWorkGroupInput{WorkGroup: aws.String("analysts")}))
	if got.WorkGroup.Description != nil {
		t.Fatalf("Description = %q, want the original (none)", aws.ToString(got.WorkGroup.Description))
	}
}

func TestUpdateWorkGroup_configurationUpdates(t *testing.T) {
	// Given: a workgroup with a result location, cutoff and encryption
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))
	must[*athena.CreateWorkGroupOutput](t, "CreateWorkGroup")(c.CreateWorkGroup(ctx, &athena.CreateWorkGroupInput{
		Name: aws.String("etl"),
		Configuration: &types.WorkGroupConfiguration{
			BytesScannedCutoffPerQuery: aws.Int64(20000000),
			ResultConfiguration: &types.ResultConfiguration{
				OutputLocation:          aws.String("s3://old/"),
				EncryptionConfiguration: &types.EncryptionConfiguration{EncryptionOption: types.EncryptionOptionSseS3},
			},
		},
	}))

	// When: it is disabled, re-described, pointed elsewhere, and loses its
	// cutoff and encryption
	must[*athena.UpdateWorkGroupOutput](t, "UpdateWorkGroup")(c.UpdateWorkGroup(ctx, &athena.UpdateWorkGroupInput{
		WorkGroup:   aws.String("etl"),
		Description: aws.String("nightly"),
		State:       types.WorkGroupStateDisabled,
		ConfigurationUpdates: &types.WorkGroupConfigurationUpdates{
			EnforceWorkGroupConfiguration:    aws.Bool(true),
			RemoveBytesScannedCutoffPerQuery: aws.Bool(true),
			EngineVersion:                    &types.EngineVersion{SelectedEngineVersion: aws.String("Athena engine version 3")},
			ResultConfigurationUpdates: &types.ResultConfigurationUpdates{
				OutputLocation:                aws.String("s3://new/"),
				RemoveEncryptionConfiguration: aws.Bool(true),
			},
		},
	}))

	// Then: every change reads back, and nothing else moved
	wg := must[*athena.GetWorkGroupOutput](t, "GetWorkGroup")(c.GetWorkGroup(ctx, &athena.GetWorkGroupInput{WorkGroup: aws.String("etl")})).WorkGroup
	cfg := wg.Configuration
	switch {
	case wg.State != types.WorkGroupStateDisabled, aws.ToString(wg.Description) != "nightly":
		t.Fatalf("State/Description = %s/%q", wg.State, aws.ToString(wg.Description))
	case !aws.ToBool(cfg.EnforceWorkGroupConfiguration), cfg.BytesScannedCutoffPerQuery != nil:
		t.Fatalf("Enforce/Cutoff = %v/%v", cfg.EnforceWorkGroupConfiguration, cfg.BytesScannedCutoffPerQuery)
	case aws.ToString(cfg.ResultConfiguration.OutputLocation) != "s3://new/", cfg.ResultConfiguration.EncryptionConfiguration != nil:
		t.Fatalf("ResultConfiguration = %+v", cfg.ResultConfiguration)
	case aws.ToString(cfg.EngineVersion.EffectiveEngineVersion) != "Athena engine version 3":
		t.Fatalf("EngineVersion = %+v", cfg.EngineVersion)
	}
}

func TestUpdateWorkGroup_invalidEngineVersion(t *testing.T) {
	// Given: primary
	c := athenaClient(t, helpers.NewTestServer(t))

	// When: it is moved to an engine that does not exist
	_, err := c.UpdateWorkGroup(context.Background(), &athena.UpdateWorkGroupInput{
		WorkGroup:            aws.String("primary"),
		ConfigurationUpdates: &types.WorkGroupConfigurationUpdates{EngineVersion: &types.EngineVersion{SelectedEngineVersion: aws.String("Athena engine version 9")}},
	})

	// Then: the update is refused
	wantAPIError(t, "UpdateWorkGroup", err, "InvalidRequestException")
}

func TestListWorkGroups_summaryShapeAndPages(t *testing.T) {
	// Given: primary and two more workgroups
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))
	for _, name := range []string{"b-team", "a-team"} {
		must[*athena.CreateWorkGroupOutput](t, "CreateWorkGroup")(c.CreateWorkGroup(ctx, &athena.CreateWorkGroupInput{Name: aws.String(name), Description: aws.String(name + " queries")}))
	}

	// When: they are listed two at a time
	var names []string
	var summaries []types.WorkGroupSummary
	p := athena.NewListWorkGroupsPaginator(c, &athena.ListWorkGroupsInput{MaxResults: aws.Int32(2)})
	for p.HasMorePages() {
		page := must[*athena.ListWorkGroupsOutput](t, "ListWorkGroups")(p.NextPage(ctx))
		for _, s := range page.WorkGroups {
			names = append(names, aws.ToString(s.Name))
			summaries = append(summaries, s)
		}
	}

	// Then: all three arrive once each, with the full summary
	if want := []string{"a-team", "b-team", "primary"}; len(names) != 3 || names[0] != want[0] || names[1] != want[1] || names[2] != want[2] {
		t.Fatalf("names = %v, want %v", names, want)
	}
	a := summaries[0]
	if aws.ToString(a.Description) != "a-team queries" || a.CreationTime == nil || a.State != types.WorkGroupStateEnabled ||
		aws.ToString(a.EngineVersion.EffectiveEngineVersion) != "Athena engine version 3" {
		t.Fatalf("summary = %+v", a)
	}
}

func TestDeleteWorkGroup_rules(t *testing.T) {
	// Given: a workgroup holding a named query
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))
	must[*athena.CreateWorkGroupOutput](t, "CreateWorkGroup")(c.CreateWorkGroup(ctx, &athena.CreateWorkGroupInput{Name: aws.String("scratch")}))
	nq := must[*athena.CreateNamedQueryOutput](t, "CreateNamedQuery")(c.CreateNamedQuery(ctx, &athena.CreateNamedQueryInput{
		Name: aws.String("q"), Database: aws.String("db"), QueryString: aws.String("SELECT 1"), WorkGroup: aws.String("scratch"),
	}))

	// When/Then: primary cannot be deleted
	_, err := c.DeleteWorkGroup(ctx, &athena.DeleteWorkGroupInput{WorkGroup: aws.String("primary")})
	wantAPIError(t, "DeleteWorkGroup primary", err, "InvalidRequestException")

	// When/Then: a non-empty workgroup needs RecursiveDeleteOption
	_, err = c.DeleteWorkGroup(ctx, &athena.DeleteWorkGroupInput{WorkGroup: aws.String("scratch")})
	wantAPIError(t, "DeleteWorkGroup non-empty", err, "InvalidRequestException")

	// When: it is deleted recursively
	must[*athena.DeleteWorkGroupOutput](t, "DeleteWorkGroup")(c.DeleteWorkGroup(ctx, &athena.DeleteWorkGroupInput{WorkGroup: aws.String("scratch"), RecursiveDeleteOption: aws.Bool(true)}))

	// Then: the workgroup and its named query are gone
	_, err = c.GetWorkGroup(ctx, &athena.GetWorkGroupInput{WorkGroup: aws.String("scratch")})
	wantAPIError(t, "GetWorkGroup after delete", err, "InvalidRequestException")
	_, err = c.GetNamedQuery(ctx, &athena.GetNamedQueryInput{NamedQueryId: nq.NamedQueryId})
	wantAPIError(t, "GetNamedQuery after recursive delete", err, "InvalidRequestException")
}

func TestListEngineVersions_autoAndVersion3(t *testing.T) {
	// Given: a fresh server
	c := athenaClient(t, helpers.NewTestServer(t))

	// When: engine versions are listed
	out := must[*athena.ListEngineVersionsOutput](t, "ListEngineVersions")(c.ListEngineVersions(context.Background(), &athena.ListEngineVersionsInput{}))

	// Then: AUTO and engine version 3, both running version 3
	if len(out.EngineVersions) != 2 {
		t.Fatalf("EngineVersions = %+v", out.EngineVersions)
	}
	for i, want := range []string{"AUTO", "Athena engine version 3"} {
		ev := out.EngineVersions[i]
		if aws.ToString(ev.SelectedEngineVersion) != want || aws.ToString(ev.EffectiveEngineVersion) != "Athena engine version 3" {
			t.Fatalf("EngineVersions[%d] = %+v", i, ev)
		}
	}
}
