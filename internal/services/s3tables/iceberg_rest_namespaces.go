package s3tables

// The Iceberg REST catalog's namespace endpoints. A namespace is the S3 Tables
// namespace of the same name; the properties the catalog keeps on it are
// Overcast's addition, since the S3 Tables API has none to show.

import (
	"context"
	"maps"
	"net/http"
	"slices"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// icebergNamespaceResponse is the spec's CreateNamespaceResponse and
// GetNamespaceResponse.
type icebergNamespaceResponse struct {
	Namespace  []string          `json:"namespace"`
	Properties map[string]string `json:"properties"`
}

func namespaceResponse(n *namespaceRecord) *icebergNamespaceResponse {
	props := maps.Clone(n.Properties)
	if props == nil {
		props = map[string]string{}
	}
	return &icebergNamespaceResponse{Namespace: []string{n.Name}, Properties: props}
}

type icebergCreateNamespaceRequest struct {
	icebergPath
	Namespace  []string          `json:"namespace"`
	Properties map[string]string `json:"properties"`
}

func (s *Service) icebergCreateNamespaceTyped(ctx context.Context, req *icebergCreateNamespaceRequest) (*icebergNamespaceResponse, *protocol.AWSError) {
	name, aerr := singleLevel(req.Namespace)
	if aerr != nil {
		return nil, aerr
	}
	_, n, aerr := s.createNamespace(ctx, req.BucketARN, name, req.Properties)
	if aerr != nil {
		return nil, aerr
	}
	return namespaceResponse(n), nil
}

func (s *Service) icebergLoadNamespaceTyped(ctx context.Context, req *icebergPath) (*icebergNamespaceResponse, *protocol.AWSError) {
	_, n, aerr := s.resolveNamespace(ctx, req.BucketARN, req.Namespace)
	if aerr != nil {
		return nil, aerr
	}
	return namespaceResponse(n), nil
}

func (s *Service) icebergNamespaceExistsTyped(ctx context.Context, req *icebergPath) *protocol.AWSError {
	_, _, aerr := s.resolveNamespace(ctx, req.BucketARN, req.Namespace)
	return aerr
}

func (s *Service) icebergDropNamespaceTyped(ctx context.Context, req *icebergPath) *protocol.AWSError {
	_, aerr := s.deleteNamespaceTyped(ctx, &namespaceRequest{TableBucketARN: req.BucketARN, Namespace: req.Namespace})
	return aerr
}

// icebergListNamespacesResponse is the spec's ListNamespacesResponse.
type icebergListNamespacesResponse struct {
	Namespaces    [][]string `json:"namespaces"`
	NextPageToken string     `json:"next-page-token,omitempty"`
}

// icebergListNamespacesTyped lists a bucket's namespaces. S3 Tables namespaces have
// one level, so a namespace given as the parent exists but has no children.
func (s *Service) icebergListNamespacesTyped(ctx context.Context, req *icebergListRequest) (*icebergListNamespacesResponse, *protocol.AWSError) {
	if req.Parent != "" {
		if _, _, aerr := s.resolveNamespace(ctx, req.BucketARN, req.Parent); aerr != nil {
			return nil, aerr
		}
		return &icebergListNamespacesResponse{Namespaces: [][]string{}}, nil
	}
	b, aerr := s.resolveBucket(ctx, req.BucketARN)
	if aerr != nil {
		return nil, aerr
	}
	namespaces, aerr := s.listNamespaces(ctx, b.Region, b.Name)
	if aerr != nil {
		return nil, aerr
	}
	names := make([][]string, 0, len(namespaces))
	for _, n := range namespaces {
		names = append(names, []string{n.Name})
	}
	page, aerr := paginate(names, req.PageSize, req.PageToken)
	if aerr != nil {
		return nil, aerr
	}
	return &icebergListNamespacesResponse{Namespaces: page.Items, NextPageToken: page.NextToken}, nil
}

type icebergNamespacePropertiesRequest struct {
	icebergPath
	Removals []string          `json:"removals"`
	Updates  map[string]string `json:"updates"`
}

// icebergNamespacePropertiesResponse is the spec's
// UpdateNamespacePropertiesResponse.
type icebergNamespacePropertiesResponse struct {
	Updated []string `json:"updated"`
	Removed []string `json:"removed"`
	Missing []string `json:"missing,omitempty"`
}

// icebergUpdateNamespacePropertiesTyped sets and removes namespace properties in
// one step. A key both set and removed is refused, as the spec requires.
func (s *Service) icebergUpdateNamespacePropertiesTyped(ctx context.Context, req *icebergNamespacePropertiesRequest) (*icebergNamespacePropertiesResponse, *protocol.AWSError) {
	for _, k := range req.Removals {
		if _, both := req.Updates[k]; both {
			return nil, icebergError(http.StatusUnprocessableEntity, "UnprocessableEntityException",
				"Property "+k+" cannot be both set and removed.")
		}
	}
	out := &icebergNamespacePropertiesResponse{Updated: slices.Sorted(maps.Keys(req.Updates)), Removed: []string{}}
	var region string
	_, aerr := readModifyWrite(s, func() (*namespaceRecord, *protocol.AWSError) {
		b, n, aerr := s.resolveNamespace(ctx, req.BucketARN, req.Namespace)
		if aerr == nil {
			region = b.Region
		}
		return n, aerr
	}, func(n *namespaceRecord) *protocol.AWSError {
		for _, k := range req.Removals {
			if _, ok := n.Properties[k]; ok {
				out.Removed = append(out.Removed, k)
				delete(n.Properties, k)
			} else {
				out.Missing = append(out.Missing, k)
			}
		}
		if len(req.Updates) > 0 && n.Properties == nil {
			n.Properties = map[string]string{}
		}
		maps.Copy(n.Properties, req.Updates)
		return nil
	}, func(n *namespaceRecord) *protocol.AWSError { return s.saveNamespace(ctx, region, n) })
	if aerr != nil {
		return nil, aerr
	}
	return out, nil
}
