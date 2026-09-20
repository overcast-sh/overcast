package eks

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

func (s *Service) listFargateProfiles(w http.ResponseWriter, r *http.Request) {
	clusterName := chi.URLParam(r, "name")
	region := s.region(r)
	ctx := r.Context()

	if _, ok := s.requireAccessibleCluster(w, r, region, clusterName); !ok {
		return
	}

	profiles, err := s.listFargateProfilesForCluster(ctx, region, clusterName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	names := make([]string, 0, len(profiles)+1)
	for _, fp := range profiles {
		names = append(names, fp.FargateProfileName)
	}
	// Always include synthetic default if not overridden by a stored profile.
	hasDefault := false
	for _, n := range names {
		if n == "default" {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		names = append(names, "default")
	}
	sort.Strings(names)
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"fargateProfileNames": names})
}

func (s *Service) describeFargateProfile(w http.ResponseWriter, r *http.Request) {
	clusterName := chi.URLParam(r, "name")
	fargateProfileName := chi.URLParam(r, "fargateProfileName")
	region := s.region(r)
	ctx := r.Context()

	cluster, ok := s.requireAccessibleCluster(w, r, region, clusterName)
	if !ok {
		return
	}

	// Check stored profiles first.
	fp, found, err := s.getFargateProfile(ctx, region, clusterName, fargateProfileName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if found {
		fp.Tags = s.readTagsForARN(ctx, fp.FargateProfileArn)
		protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"fargateProfile": fp})
		return
	}

	// Fall back to synthetic "default" profile.
	if fargateProfileName != "default" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    fmt.Sprintf("No Fargate profile found for name: %s", fargateProfileName),
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	profile := &FargateProfile{
		ClusterName:         clusterName,
		FargateProfileName:  fargateProfileName,
		FargateProfileArn:   s.fargateProfileARN(region, clusterName, fargateProfileName),
		CreatedAt:           cluster.CreatedAt,
		Status:              "ACTIVE",
		PodExecutionRoleArn: fmt.Sprintf("arn:aws:iam::%s:role/eks-fargate-pod-execution-role", s.accountID()),
		Selectors: []map[string]any{
			{"namespace": "default"},
		},
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"fargateProfile": profile})
}

func (s *Service) createFargateProfile(w http.ResponseWriter, r *http.Request) {
	clusterName := chi.URLParam(r, "name")
	region := s.region(r)
	ctx := r.Context()

	if _, ok := s.requireAccessibleCluster(w, r, region, clusterName); !ok {
		return
	}

	var req struct {
		FargateProfileName  string            `json:"fargateProfileName"`
		PodExecutionRoleArn string            `json:"podExecutionRoleArn"`
		Subnets             []string          `json:"subnets"`
		Selectors           []map[string]any  `json:"selectors"`
		Tags                map[string]string `json:"tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.FargateProfileName, "fargateProfileName") {
		return
	}
	if !serviceutil.RequireString(w, r, req.PodExecutionRoleArn, "podExecutionRoleArn") {
		return
	}

	fp := &FargateProfile{
		ClusterName:         clusterName,
		FargateProfileName:  req.FargateProfileName,
		FargateProfileArn:   s.fargateProfileARN(region, clusterName, req.FargateProfileName),
		CreatedAt:           s.clk.Now(),
		Status:              "ACTIVE",
		PodExecutionRoleArn: req.PodExecutionRoleArn,
		Subnets:             req.Subnets,
		Selectors:           req.Selectors,
	}
	if err := s.putFargateProfile(ctx, region, fp); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if err := s.putInlineTags(ctx, fp.FargateProfileArn, req.Tags); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	fp.Tags = s.readTagsForARN(ctx, fp.FargateProfileArn)
	protocol.WriteJSON(w, r, http.StatusCreated, map[string]any{"fargateProfile": fp})
}

func (s *Service) deleteFargateProfile(w http.ResponseWriter, r *http.Request) {
	clusterName := chi.URLParam(r, "name")
	fargateProfileName := chi.URLParam(r, "fargateProfileName")
	region := s.region(r)
	ctx := r.Context()

	if _, ok := s.requireAccessibleCluster(w, r, region, clusterName); !ok {
		return
	}

	fp, found, err := s.getFargateProfile(ctx, region, clusterName, fargateProfileName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    fmt.Sprintf("No Fargate profile found for name: %s", fargateProfileName),
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	if err := s.store.Delete(ctx, nsFargate, fargateProfileKey(region, clusterName, fargateProfileName)); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if err := s.store.Delete(ctx, nsTags, tagKey(fp.FargateProfileArn)); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	fp.Status = "DELETING"
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"fargateProfile": fp})
}
