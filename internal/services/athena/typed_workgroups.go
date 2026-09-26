package athena

import (
	"context"
	"regexp"
	"sort"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// primaryWorkGroup is the workgroup every account has, that every operation
// taking an optional WorkGroup falls back to, and that cannot be deleted.
const primaryWorkGroup = "primary"

const (
	workGroupEnabled  = "ENABLED"
	workGroupDisabled = "DISABLED"
)

// workGroupNamePattern is the model's WorkGroupName pattern.
var workGroupNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)

// athenaTagCfg tunes shared tag validation to Athena's error shape.
var athenaTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    codeInvalidRequest,
	InvalidCode:     codeInvalidRequest,
	ExceededMessage: "Too many tags. A resource can hold a maximum of 50 tags.",
}

// orPrimary is the workgroup an operation acts on when the caller named none.
func orPrimary(name string) string {
	if name == "" {
		return primaryWorkGroup
	}
	return name
}

func workGroupLockKey(name string) string { return "workgroup:" + name }

// loadWorkGroup reads a workgroup, seeding primary the first time anything
// asks for it. It takes no record lock, so a caller may hold one.
func (s *Service) loadWorkGroup(ctx context.Context, name string) (*workGroupRecord, *protocol.AWSError) {
	wg, err := s.store.getWorkGroup(ctx, name)
	if err != nil {
		return nil, errInternal(err)
	}
	if wg != nil {
		return wg, nil
	}
	if name == primaryWorkGroup {
		return s.seedPrimary(ctx)
	}
	return nil, errWorkGroupNotFound(name)
}

// seedPrimary writes the primary workgroup if it is missing. It is seeded on
// first use rather than at startup, which must not touch the store, and so
// also comes back after a state reset.
func (s *Service) seedPrimary(ctx context.Context) (*workGroupRecord, *protocol.AWSError) {
	s.primaryMu.Lock()
	defer s.primaryMu.Unlock()
	if wg, err := s.store.getWorkGroup(ctx, primaryWorkGroup); err != nil || wg != nil {
		return wg, internalOrNil(err)
	}
	wg := &workGroupRecord{WorkGroup: WorkGroup{
		Name:          primaryWorkGroup,
		State:         workGroupEnabled,
		CreationTime:  s.now(),
		Configuration: primaryConfiguration(),
	}}
	if err := s.store.putWorkGroup(ctx, wg); err != nil {
		return nil, errInternal(err)
	}
	return wg, nil
}

// primaryConfiguration is the configuration AWS reports for an account's
// untouched primary workgroup: nothing enforced, no result location, and the
// engine chosen automatically.
func primaryConfiguration() *WorkGroupConfiguration {
	return &WorkGroupConfiguration{
		ResultConfiguration:                  &ResultConfiguration{},
		EnforceWorkGroupConfiguration:        ptr(false),
		PublishCloudWatchMetricsEnabled:      ptr(false),
		RequesterPaysEnabled:                 ptr(false),
		EnableMinimumEncryptionConfiguration: ptr(false),
		EngineVersion:                        &EngineVersion{SelectedEngineVersion: engineVersionAuto, EffectiveEngineVersion: engineVersion3},
	}
}

func internalOrNil(err error) *protocol.AWSError {
	if err == nil {
		return nil
	}
	return errInternal(err)
}

// ─── Operations ───────────────────────────────────────────────

type createWorkGroupReq struct {
	Name          string                  `json:"Name"`
	Description   string                  `json:"Description"`
	Configuration *WorkGroupConfiguration `json:"Configuration"`
	Tags          []serviceutil.TagPair   `json:"Tags"`
}

type workGroupNameReq struct {
	WorkGroup string `json:"WorkGroup"`
}

type getWorkGroupResp struct {
	WorkGroup WorkGroup `json:"WorkGroup"`
}

type listWorkGroupsReq struct {
	MaxResults int32  `json:"MaxResults"`
	NextToken  string `json:"NextToken"`
}

type listWorkGroupsResp struct {
	WorkGroups []WorkGroupSummary `json:"WorkGroups"`
	NextToken  string             `json:"NextToken,omitempty"`
}

type updateWorkGroupReq struct {
	WorkGroup            string                         `json:"WorkGroup"`
	Description          *string                        `json:"Description"`
	State                string                         `json:"State"`
	ConfigurationUpdates *WorkGroupConfigurationUpdates `json:"ConfigurationUpdates"`
}

type deleteWorkGroupReq struct {
	WorkGroup             string `json:"WorkGroup"`
	RecursiveDeleteOption *bool  `json:"RecursiveDeleteOption"`
}

func (s *Service) createWorkGroupTyped(ctx context.Context, req *createWorkGroupReq) (*struct{}, *protocol.AWSError) {
	if !workGroupNamePattern.MatchString(req.Name) {
		return nil, errInvalidRequest("WorkGroup name %q is not valid: it must match [a-zA-Z0-9._-]{1,128}.", req.Name)
	}
	tags := serviceutil.TagsFromList(req.Tags)
	if aerr := serviceutil.ValidateTags(athenaTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	cfg := req.Configuration
	if cfg == nil {
		cfg = &WorkGroupConfiguration{}
	}
	if aerr := validateConfiguration(cfg); aerr != nil {
		return nil, aerr
	}
	defer s.lock(workGroupLockKey(req.Name))()
	existing, err := s.store.getWorkGroup(ctx, req.Name)
	if err != nil {
		return nil, errInternal(err)
	}
	if existing != nil || req.Name == primaryWorkGroup {
		return nil, errInvalidRequest("WorkGroup %s is already created.", req.Name)
	}
	wg := &workGroupRecord{
		WorkGroup: WorkGroup{
			Name: req.Name, State: workGroupEnabled, Description: req.Description,
			CreationTime: s.now(), Configuration: cfg,
		},
		Tags: tags,
	}
	if err := s.store.putWorkGroup(ctx, wg); err != nil {
		return nil, errInternal(err)
	}
	return &struct{}{}, nil
}

func (s *Service) getWorkGroupTyped(ctx context.Context, req *workGroupNameReq) (*getWorkGroupResp, *protocol.AWSError) {
	if req.WorkGroup == "" {
		return nil, errRequired("WorkGroup")
	}
	wg, aerr := s.loadWorkGroup(ctx, req.WorkGroup)
	if aerr != nil {
		return nil, aerr
	}
	return &getWorkGroupResp{WorkGroup: wg.WorkGroup}, nil
}

func (s *Service) listWorkGroupsTyped(ctx context.Context, req *listWorkGroupsReq) (*listWorkGroupsResp, *protocol.AWSError) {
	if _, aerr := s.loadWorkGroup(ctx, primaryWorkGroup); aerr != nil {
		return nil, aerr
	}
	records, err := s.store.listWorkGroups(ctx)
	if err != nil {
		return nil, errInternal(err)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	summaries := make([]WorkGroupSummary, len(records))
	for i, wg := range records {
		summaries[i] = WorkGroupSummary{
			Name: wg.Name, State: wg.State, Description: wg.Description,
			CreationTime: wg.CreationTime, EngineVersion: wg.engineVersion(),
		}
	}
	page, aerr := paginate(summaries, req.MaxResults, req.NextToken, workGroupsPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &listWorkGroupsResp{WorkGroups: page.Items, NextToken: page.NextToken}, nil
}

// engineVersion is the workgroup's engine; normalize guarantees one.
func (wg *WorkGroup) engineVersion() *EngineVersion { return wg.Configuration.EngineVersion }

// normalize gives a record written before workgroups carried an engine
// version the one AWS reports for a workgroup that chose none: AUTO.
func (wg *WorkGroup) normalize() {
	if wg.Configuration == nil {
		wg.Configuration = &WorkGroupConfiguration{}
	}
	if wg.Configuration.EngineVersion == nil {
		wg.Configuration.EngineVersion, _ = resolveEngineVersion(nil)
	}
}

func (s *Service) updateWorkGroupTyped(ctx context.Context, req *updateWorkGroupReq) (*struct{}, *protocol.AWSError) {
	if req.WorkGroup == "" {
		return nil, errRequired("WorkGroup")
	}
	if req.State != "" && req.State != workGroupEnabled && req.State != workGroupDisabled {
		return nil, errInvalidRequest("State must be one of %s or %s.", workGroupEnabled, workGroupDisabled)
	}
	defer s.lock(workGroupLockKey(req.WorkGroup))()
	wg, aerr := s.loadWorkGroup(ctx, req.WorkGroup)
	if aerr != nil {
		return nil, aerr
	}
	if req.ConfigurationUpdates != nil {
		cfg, aerr := applyConfigurationUpdates(wg.Configuration, req.ConfigurationUpdates)
		if aerr != nil {
			return nil, aerr
		}
		wg.Configuration = cfg
	}
	if req.Description != nil {
		wg.Description = *req.Description
	}
	if req.State != "" {
		wg.State = req.State
	}
	if err := s.store.putWorkGroup(ctx, wg); err != nil {
		return nil, errInternal(err)
	}
	return &struct{}{}, nil
}

// deleteWorkGroupTyped removes a workgroup. "The primary workgroup cannot be
// deleted." Named queries keep a workgroup from being deleted unless
// RecursiveDeleteOption is set — the one kind of content both the API and
// the CloudFormation reference name — and a recursive delete also removes the
// workgroup's query executions, cancelling any still running. Prepared
// statements go with the workgroup either way: nothing else can reach them,
// and a later workgroup of the same name must not inherit them.
//
// contentsMu is taken before the workgroup's record lock, the order every
// content write takes them in, so no named query or prepared statement can
// be written into the workgroup while it is being emptied.
func (s *Service) deleteWorkGroupTyped(ctx context.Context, req *deleteWorkGroupReq) (*struct{}, *protocol.AWSError) {
	if req.WorkGroup == "" {
		return nil, errRequired("WorkGroup")
	}
	if req.WorkGroup == primaryWorkGroup {
		return nil, errInvalidRequest("The primary workgroup cannot be deleted.")
	}
	s.contentsMu.Lock()
	defer s.contentsMu.Unlock()
	defer s.lock(workGroupLockKey(req.WorkGroup))()
	if _, aerr := s.loadWorkGroup(ctx, req.WorkGroup); aerr != nil {
		return nil, aerr
	}
	recursive := req.RecursiveDeleteOption != nil && *req.RecursiveDeleteOption
	contents, aerr := s.workGroupContents(ctx, req.WorkGroup, recursive)
	if aerr != nil {
		return nil, aerr
	}
	if len(contents.namedQueries) > 0 && !recursive {
		return nil, errInvalidRequest("WorkGroup %s is not empty. Set RecursiveDeleteOption to delete it with its named queries.", req.WorkGroup)
	}
	for _, qe := range contents.running {
		s.executor.Cancel(ctx, qe)
	}
	if err := s.deleteContents(ctx, contents); err != nil {
		return nil, errInternal(err)
	}
	if recursive {
		s.store.forgetRecentQueries(ctx, req.WorkGroup)
	}
	if err := s.store.delete(ctx, nsWorkGroups, req.WorkGroup); err != nil {
		return nil, errInternal(err)
	}
	return &struct{}{}, nil
}

// workGroupContents is what a delete removes with a workgroup, by key, and
// which of the executions are still running.
type workGroupContents struct {
	namedQueries       []string
	preparedStatements []string
	queryExecutions    []string
	running            []string
}

// workGroupContents lists a workgroup's named queries and prepared
// statements, and, for a recursive delete, its query executions.
func (s *Service) workGroupContents(ctx context.Context, name string, withQueries bool) (workGroupContents, *protocol.AWSError) {
	var c workGroupContents
	named, err := scan[NamedQuery](ctx, s.store, nsNamedQueries, "")
	if err != nil {
		return c, errInternal(err)
	}
	for _, nq := range named {
		if nq.WorkGroup == name {
			c.namedQueries = append(c.namedQueries, nq.NamedQueryId)
		}
	}
	statements, err := scan[PreparedStatement](ctx, s.store, nsPreparedStatements, preparedStatementKey(name, ""))
	if err != nil {
		return c, errInternal(err)
	}
	for _, ps := range statements {
		c.preparedStatements = append(c.preparedStatements, preparedStatementKey(name, ps.StatementName))
	}
	if !withQueries {
		return c, nil
	}
	queries, err := s.store.listQueries(ctx)
	if err != nil {
		return c, errInternal(err)
	}
	for _, qe := range queries {
		if qe.WorkGroup != name {
			continue
		}
		c.queryExecutions = append(c.queryExecutions, qe.QueryExecutionId)
		if !isTerminal(qe.Status.State) {
			c.running = append(c.running, qe.QueryExecutionId)
		}
	}
	return c, nil
}

func (s *Service) deleteContents(ctx context.Context, c workGroupContents) error {
	for ns, keys := range map[string][]string{
		nsNamedQueries:       c.namedQueries,
		nsPreparedStatements: c.preparedStatements,
		nsQueries:            c.queryExecutions,
	} {
		for _, key := range keys {
			if err := s.store.delete(ctx, ns, key); err != nil {
				return err
			}
		}
	}
	return nil
}
