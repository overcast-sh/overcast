package eks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"go.uber.org/zap"
)

func (s *Service) createCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                    string            `json:"name"`
		RoleArn                 string            `json:"roleArn"`
		Version                 string            `json:"version"`
		ResourcesVPCConfig      map[string]any    `json:"resourcesVpcConfig"`
		KubernetesNetworkConfig map[string]any    `json:"kubernetesNetworkConfig"`
		EncryptionConfig        []map[string]any  `json:"encryptionConfig"`
		Logging                 map[string]any    `json:"logging"`
		AccessConfig            map[string]any    `json:"accessConfig"`
		Tags                    map[string]string `json:"tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.Name, "name") || !serviceutil.RequireString(w, r, req.RoleArn, "roleArn") {
		return
	}

	region := s.region(r)
	ctx := r.Context()
	if _, found, err := s.getCluster(ctx, region, req.Name); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	} else if found {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceInUseException",
			Message:    fmt.Sprintf("Cluster %q already exists", req.Name),
			HTTPStatus: http.StatusConflict,
		})
		return
	}

	version := req.Version
	if version == "" {
		version = "1.31"
	}
	cluster := &Cluster{
		Name:                    req.Name,
		Arn:                     s.clusterARN(region, req.Name),
		CreatedAt:               s.clk.Now(),
		Version:                 version,
		RoleArn:                 req.RoleArn,
		ResourcesVPCConfig:      req.ResourcesVPCConfig,
		KubernetesNetworkConfig: req.KubernetesNetworkConfig,
		EncryptionConfig:        req.EncryptionConfig,
		Logging:                 req.Logging,
		AccessConfig:            req.AccessConfig,
	}
	if s.liveModeEnabled() {
		if !s.dockerReady.Load() {
			s.writeLiveModeRequiresDocker(w, r)
			return
		}
		cluster.Status = "CREATING"
		cluster.Endpoint = ""
		cluster.CertificateAuthority = map[string]any{}
	} else {
		cluster.Endpoint = fmt.Sprintf("https://%s.mock.eks.local", req.Name)
		cluster.Status = "ACTIVE"
		cluster.CertificateAuthority = map[string]any{
			"data": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0t",
		}
	}
	if err := s.putCluster(ctx, region, cluster); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if err := s.putInlineTags(ctx, cluster.Arn, req.Tags); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	cluster.Tags = s.readTagsForARN(ctx, cluster.Arn)
	if s.liveModeEnabled() {
		// launchLiveBootstrap owns the liveWg / liveStopping fence against a
		// concurrent Stop (#1291) and the hand-off to the bootstrap goroutine.
		// Once shutdown has begun it refuses: no runtime is registered and no
		// bootstrap goroutine runs, so this can never leave a k3s container
		// starting past shutdown. The cluster record already written above
		// then goes terminal — never a half-started container — using the
		// same failure path and "shut down while starting" wording an
		// in-flight bootstrap gets when Stop cancels it out from under it,
		// so callers see one consistent outcome via DescribeCluster either
		// way.
		if !s.launchLiveBootstrap(region, cluster, nil) {
			s.failLiveCluster(ctx, region, cluster.Name, issueInternalFailure, context.Canceled)
		}
	}

	protocol.WriteJSON(w, r, http.StatusCreated, map[string]any{"cluster": cluster})
}

func (s *Service) describeCluster(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	region := s.region(r)
	cluster, ok := s.requireAccessibleCluster(w, r, region, name)
	if !ok {
		return
	}
	if s.liveModeEnabled() {
		cluster = s.reconcileReadyLiveCluster(r.Context(), region, cluster)
	}
	// The endpoint is minted for this caller, not replayed from the record:
	// the stored form is canonical, and which address is dialable depends on
	// who is asking — see clusterEndpointFor.
	cluster = s.renderCluster(r.Context(), region, cluster)
	cluster.Tags = s.readTagsForARN(r.Context(), cluster.Arn)
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"cluster": cluster})
}

func (s *Service) listClusters(w http.ResponseWriter, r *http.Request) {
	region := s.region(r)
	pairs, err := s.store.Scan(r.Context(), nsClusters, serviceutil.RegionKey(region, ""))
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	clusters := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		var c Cluster
		if err := json.Unmarshal([]byte(kv.Value), &c); err != nil {
			continue
		}
		if s.liveModeEnabled() && s.isMockModeClusterRecord(&c) {
			continue
		}
		clusters = append(clusters, c.Name)
	}
	sort.Strings(clusters)
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"clusters": clusters})
}

func (s *Service) listUpdates(w http.ResponseWriter, r *http.Request) {
	clusterName := chi.URLParam(r, "name")
	region := s.region(r)
	ctx := r.Context()

	if _, ok := s.requireAccessibleCluster(w, r, region, clusterName); !ok {
		return
	}

	pairs, err := s.store.Scan(ctx, nsUpdates, serviceutil.RegionKey(region, clusterName+"/"))
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	ids := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		var u Update
		if err := json.Unmarshal([]byte(kv.Value), &u); err != nil {
			continue
		}
		if u.ID != "" {
			ids = append(ids, u.ID)
		}
	}
	sort.Strings(ids)

	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"updateIds": ids})
}

// listInsights serves POST /clusters/{clusterName}/insights. AWS models a POST
// because filter, maxResults and nextToken are body members; Overcast served a
// GET, with none of them, until #858.
func (s *Service) listInsights(w http.ResponseWriter, r *http.Request) {
	req := &listInsightsRequest{}
	if !serviceutil.DecodeJSON(w, r, req) {
		return
	}
	// The label is applied after the body so a body member of the same name
	// cannot displace the path the request was routed on.
	req.ClusterName = chi.URLParam(r, "name")
	out, aerr := s.listInsightsTyped(r.Context(), req)
	writeResult(w, r, out, aerr)
}

func (s *Service) describeInsight(w http.ResponseWriter, r *http.Request) {
	clusterName := chi.URLParam(r, "name")
	insightID := chi.URLParam(r, "insightId")
	region := s.region(r)

	if _, ok := s.requireAccessibleCluster(w, r, region, clusterName); !ok {
		return
	}

	for _, insight := range syntheticClusterInsights(clusterName) {
		if insight["id"] == insightID {
			protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"insight": insight})
			return
		}
	}

	protocol.WriteJSONError(w, r, &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    fmt.Sprintf("No insight found for id: %s", insightID),
		HTTPStatus: http.StatusNotFound,
	})
}

func syntheticClusterInsights(clusterName string) []map[string]any {
	return []map[string]any{
		{
			"id":                "platform-version-check",
			"name":              "PlatformVersionReview",
			"category":          "UPGRADE_READINESS",
			"kubernetesVersion": "1.29",
			"description":       fmt.Sprintf("Cluster %s is running a supported platform version.", clusterName),
			"insightStatus": map[string]any{
				"status": "PASSING",
				"reason": "No action required",
			},
		},
	}
}

func (s *Service) describeClusterVersions(w http.ResponseWriter, r *http.Request) {
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"clusterVersions": []map[string]any{
			{
				"clusterVersion": "1.27",
				"defaultVersion": false,
				"status":         "STANDARD_SUPPORT",
			},
			{
				"clusterVersion": "1.28",
				"defaultVersion": false,
				"status":         "STANDARD_SUPPORT",
			},
			{
				"clusterVersion": "1.29",
				"defaultVersion": true,
				"status":         "STANDARD_SUPPORT",
			},
		},
	})
}

func (s *Service) updateClusterVersion(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	region := s.region(r)
	ctx := r.Context()

	cluster, ok := s.requireAccessibleCluster(w, r, region, name)
	if !ok {
		return
	}

	var req struct {
		Version string `json:"version"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.Version, "version") {
		return
	}

	cluster.Version = req.Version
	if err := s.putCluster(ctx, region, cluster); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	update := &Update{
		ID:        fmt.Sprintf("upd-%s-%d", name, s.clk.Now().UnixNano()),
		Status:    "Successful",
		Type:      "VersionUpdate",
		CreatedAt: s.clk.Now(),
		Params: []map[string]any{
			{"type": "Version", "value": req.Version},
		},
	}
	if err := s.putUpdate(ctx, region, name, update); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"update": update,
	})
}

func (s *Service) describeUpdate(w http.ResponseWriter, r *http.Request) {
	clusterName := chi.URLParam(r, "name")
	updateID := chi.URLParam(r, "updateId")
	region := s.region(r)
	ctx := r.Context()

	if _, ok := s.requireAccessibleCluster(w, r, region, clusterName); !ok {
		return
	}

	update, found, err := s.getUpdate(ctx, region, clusterName, updateID)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    fmt.Sprintf("No update found for id: %s", updateID),
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"update": update})
}

func (s *Service) updateKubeconfig(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	region := s.region(r)

	cluster, ok := s.requireAccessibleCluster(w, r, region, name)
	if !ok {
		return
	}

	if s.liveModeEnabled() {
		cluster = s.reconcileReadyLiveCluster(r.Context(), region, cluster)
		if cluster.Status != "ACTIVE" || strings.TrimSpace(cluster.Endpoint) == "" {
			protocol.WriteJSONError(w, r, &protocol.AWSError{
				Code:       "ServiceUnavailableException",
				Message:    "Cluster is not ready yet. Wait for status ACTIVE before requesting kubeconfig.",
				HTTPStatus: http.StatusServiceUnavailable,
			})
			return
		}
	}

	caData, _ := cluster.CertificateAuthority["data"].(string)
	if s.liveModeEnabled() && strings.TrimSpace(caData) == "" {
		runtime, found := s.getLiveClusterRuntime(region, name)
		if !found {
			var reconcileErr error
			runtime, found, reconcileErr = s.reconcileLiveClusterRuntime(r.Context(), region, name)
			if reconcileErr != nil {
				protocol.WriteJSONError(w, r, protocol.ErrInternalError)
				return
			}
		}
		if found && strings.TrimSpace(runtime.containerID) != "" {
			if extracted, extractErr := s.extractK3sCAData(r.Context(), runtime.containerID); extractErr == nil && strings.TrimSpace(extracted) != "" {
				caData = extracted
				cluster.CertificateAuthority = map[string]any{"data": caData}
				if putErr := s.putCluster(r.Context(), region, cluster); putErr != nil {
					protocol.WriteJSONError(w, r, protocol.ErrInternalError)
					return
				}
			}
		}
		if strings.TrimSpace(caData) == "" {
			protocol.WriteJSONError(w, r, &protocol.AWSError{
				Code:       "ServiceUnavailableException",
				Message:    "Cluster certificate authority data is not ready yet. Try again shortly.",
				HTTPStatus: http.StatusServiceUnavailable,
			})
			return
		}
	}

	// The server the file names is the endpoint as this caller can dial it —
	// a kubeconfig fetched from inside a container is used from there.
	server := s.clusterEndpointFor(r.Context(), region, cluster)
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: %s
  cluster:
    server: %s
    certificate-authority-data: %s
contexts:
- name: %s
  context:
    cluster: %s
    user: %s
current-context: %s
users:
- name: %s
  user:
    token: overcast-dev-token
`, cluster.Name, server, caData, cluster.Name, cluster.Name, cluster.Name, cluster.Name, cluster.Name)

	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"kubeconfig": kubeconfig,
	})
}

func (s *Service) deleteCluster(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	region := s.region(r)
	ctx := r.Context()
	cluster, found, err := s.getCluster(ctx, region, name)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    fmt.Sprintf("No cluster found for name: %s", name),
			HTTPStatus: http.StatusNotFound,
		})
		return
	}
	if s.liveModeEnabled() {
		runtime, runtimeFound := s.deleteLiveClusterRuntime(region, name)
		if !runtimeFound || strings.TrimSpace(runtime.containerID) == "" {
			runtime, runtimeFound, err = s.reconcileLiveClusterRuntime(ctx, region, name)
			if err != nil {
				log := s.log.WithRecorder(ctx)
				log.Warn("failed to reconcile live cluster runtime during delete; skipping container cleanup",
					zap.String("cluster", name), zap.Error(err))
				runtimeFound = false
			}
			if runtimeFound {
				_, _ = s.deleteLiveClusterRuntime(region, name)
			}
		} else {
			// Refresh by deterministic managed name so stale cached IDs do not
			// block cleanup when the underlying container was recreated.
			reconciled, reconciledFound, reconcileErr := s.reconcileLiveClusterRuntime(ctx, region, name)
			if reconcileErr != nil {
				log := s.log.WithRecorder(ctx)
				log.Warn("failed to reconcile stale live cluster runtime during delete; using cached runtime",
					zap.String("cluster", name), zap.Error(reconcileErr))
			} else {
				if reconciledFound {
					runtime = reconciled
					runtimeFound = true
					_, _ = s.deleteLiveClusterRuntime(region, name)
				}
			}
		}
		if runtimeFound {
			if err := s.cleanupLiveClusterRuntime(ctx, runtime); err != nil {
				protocol.WriteJSONError(w, r, protocol.ErrInternalError)
				return
			}
		}
	}

	if err := s.store.Delete(ctx, nsClusters, clusterKey(region, name)); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	clusterPrefix := serviceutil.RegionKey(region, name+"/")
	for _, ns := range []string{nsNodegroups, nsUpdates, nsFargate, nsAddons, nsIDPConfigs, nsAccess, nsAccessPol, nsPodIDAssoc} {
		if err := s.deleteNamespaceByPrefix(ctx, ns, clusterPrefix); err != nil {
			protocol.WriteJSONError(w, r, protocol.ErrInternalError)
			return
		}
	}
	if err := s.store.Delete(ctx, nsTags, tagKey(cluster.Arn)); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	arnPrefixBase := fmt.Sprintf("arn:aws:eks:%s:%s:", region, s.accountID())
	for _, tagPrefix := range []string{
		arnPrefixBase + "nodegroup/" + name + "/",
		arnPrefixBase + "fargateprofile/" + name + "/",
		arnPrefixBase + "addon/" + name + "/",
		arnPrefixBase + "identityproviderconfig/" + name + "/",
		arnPrefixBase + "access-entry/" + name + "/",
		arnPrefixBase + "podidentityassociation/" + name + "/",
	} {
		if err := s.deleteNamespaceByPrefix(ctx, nsTags, tagPrefix); err != nil {
			protocol.WriteJSONError(w, r, protocol.ErrInternalError)
			return
		}
	}
	cluster.Status = "DELETING"
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"cluster": cluster})
}

func (s *Service) updateClusterConfig(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	region := s.region(r)
	ctx := r.Context()

	cluster, ok := s.requireAccessibleCluster(w, r, region, name)
	if !ok {
		return
	}

	var req struct {
		Logging                 map[string]any `json:"logging"`
		ResourcesVPCConfig      map[string]any `json:"resourcesVpcConfig"`
		KubernetesNetworkConfig map[string]any `json:"kubernetesNetworkConfig"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	updateType := "ConfigUpdate"
	var params []map[string]any

	if req.Logging != nil {
		cluster.Logging = req.Logging
		updateType = "LoggingUpdate"
		params = append(params, map[string]any{"type": "ClusterLogging", "value": "updated"})
	}
	if req.ResourcesVPCConfig != nil {
		cluster.ResourcesVPCConfig = req.ResourcesVPCConfig
		if updateType == "ConfigUpdate" {
			updateType = "VpcConfigUpdate"
		}
		params = append(params, map[string]any{"type": "VpcConfig", "value": "updated"})
	}
	if req.KubernetesNetworkConfig != nil {
		cluster.KubernetesNetworkConfig = req.KubernetesNetworkConfig
		params = append(params, map[string]any{"type": "KubernetesNetworkConfig", "value": "updated"})
	}
	if len(params) == 0 {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "No configuration changes requested",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	if err := s.putCluster(ctx, region, cluster); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	update := &Update{
		ID:        fmt.Sprintf("upd-%s-%d", name, s.clk.Now().UnixNano()),
		Status:    "Successful",
		Type:      updateType,
		CreatedAt: s.clk.Now(),
		Params:    params,
	}
	if err := s.putUpdate(ctx, region, name, update); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"update": update})
}
