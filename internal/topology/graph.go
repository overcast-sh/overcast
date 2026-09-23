package topology

import (
	"context"
	"slices"
	"strings"
)

// Contributor is implemented by a service that puts resources on the system
// map. The router collects every router.Service that implements it and calls
// each one concurrently, with its own Graph, on every GET /_overcast/topology.
//
// A contributor reads only its own state: nodes for its resources, and edges
// for the relationships its state records (a queue's redrive policy, a
// function's event source mappings). An edge to another service's resource
// names it by Ref and is resolved by Build.
type Contributor interface {
	ContributeTopology(ctx context.Context, g *Graph) error
}

// NodeID is the ID of the node for a resource: "<region>::<kind>::<name>".
// kind is usually the node's Service, but a service with several node kinds
// uses one per kind (ECS uses "ecs", "ecs-service" and "ecs-task").
func NodeID(region, kind, name string) string {
	return region + "::" + kind + "::" + name
}

type refKind uint8

const (
	refNone refKind = iota
	refNode
	refCFN
	refImage
)

// Ref names an edge endpoint without knowing whether, or where, the node
// exists. The zero Ref never resolves.
type Ref struct {
	kind      refKind
	key       string
	anyRegion bool
}

// ID refers to the node NodeID(region, kind, name). It resolves only to that
// exact node unless AnyRegion is applied.
func ID(region, kind, name string) Ref {
	return Ref{kind: refNode, key: NodeID(region, kind, name)}
}

// AnyRegion lets a node ref fall back to the same kind and name in another
// region when the exact node does not exist, for relationships whose stored
// ARN may name a stale region (an event source mapping imported from another
// stack, say). When several regions match, the lowest region name wins, so
// the result does not depend on scan order. It has no effect on other refs.
func (r Ref) AnyRegion() Ref {
	if r.kind == refNode {
		r.anyRegion = true
	}
	return r
}

// CFN refers to whichever node registered itself under this CloudFormation
// resource type and physical resource ID, the pair a stack records for each
// resource it provisions. region is the stack's region: some physical IDs
// (a Lambda function's name) are unique only within one.
func CFN(region, resourceType, physicalID string) Ref {
	return Ref{kind: refCFN, key: region + "\x00" + resourceType + "\x00" + physicalID}
}

// Image refers to whichever node registered itself as the repository of a
// container image. The reference is normalised first, so a tag, a digest and
// a URL scheme do not matter: "https://host/repo:tag" and "host/repo@sha256:…"
// both name the repository "host/repo". An empty image is the zero Ref.
func Image(image string) Ref {
	repo := normalizeImage(image)
	if repo == "" {
		return Ref{}
	}
	return Ref{kind: refImage, key: repo}
}

func normalizeImage(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	image = strings.TrimPrefix(strings.TrimPrefix(image, "https://"), "http://")
	if idx := strings.IndexByte(image, '@'); idx >= 0 {
		image = image[:idx]
	}
	lastSlash := strings.LastIndexByte(image, '/')
	lastColon := strings.LastIndexByte(image, ':')
	if lastColon > lastSlash {
		image = image[:lastColon]
	}
	return image
}

// Link is an edge between two refs, as a contributor writes it. Build turns
// it into an Edge once both endpoints resolve, and drops it otherwise.
type Link struct {
	Source, Target Ref
	// ID names the edge. When empty, Build derives
	// IDPrefix + "::" + source + "→" + target from the resolved node IDs.
	ID       string
	IDPrefix string
	Type     string
	Label    string
	State    string
}

type anchoredNode struct {
	node    Node
	anchors []Ref
}

type alias struct {
	ref    Ref
	nodeID string
}

type stackTag struct {
	ref       Ref
	stackName string
}

// Graph collects one contributor's part of the map. It is not safe for
// concurrent use: give each contributor its own and merge them with Build.
type Graph struct {
	nodes    []Node
	anchored []anchoredNode
	aliases  []alias
	links    []Link
	edges    []Edge
	stacks   []stackTag
}

// AddNode adds a node, and registers it under each alias (CFN or Image refs)
// so other services' edges can find it without knowing its ID.
func (g *Graph) AddNode(n Node, aliases ...Ref) {
	g.nodes = append(g.nodes, n)
	for _, r := range aliases {
		if r.kind != refNone && r.kind != refNode {
			g.aliases = append(g.aliases, alias{ref: r, nodeID: n.ID})
		}
	}
}

// AddAnchoredNode adds a node that only means something between other nodes,
// such as an event source mapping's filter. It is kept only when every anchor
// resolves.
func (g *Graph) AddAnchoredNode(n Node, anchors ...Ref) {
	g.anchored = append(g.anchored, anchoredNode{node: n, anchors: anchors})
}

// AddLink adds an edge between two refs.
func (g *Graph) AddLink(l Link) {
	g.links = append(g.links, l)
}

// AddEdge adds an edge verbatim, without resolving its endpoints. It is for
// edges between the phantom group nodes the web layout creates (a
// CloudFormation stack's "stack::<region>::<name>"), which are never nodes of
// the graph.
func (g *Graph) AddEdge(e Edge) {
	g.edges = append(g.edges, e)
}

// SetStack records that the resource r refers to belongs to a CloudFormation
// stack. Build copies the name onto the resolved node's StackName.
func (g *Graph) SetStack(r Ref, stackName string) {
	g.stacks = append(g.stacks, stackTag{ref: r, stackName: stackName})
}

// Build merges the contributors' graphs into the response. With a non-empty
// regionFilter only nodes in that region are kept, and only edges whose
// endpoints were both kept; the filter region is listed even when it holds no
// nodes.
func Build(regionFilter string, graphs ...*Graph) Response {
	nodes := []Node{}
	index := make(map[string]int) // node ID → position in nodes
	add := func(n Node) {
		if regionFilter != "" && n.Region != regionFilter {
			return
		}
		if _, dup := index[n.ID]; !dup {
			index[n.ID] = len(nodes)
		}
		nodes = append(nodes, n)
	}
	for _, g := range graphs {
		for _, n := range g.nodes {
			add(n)
		}
	}

	r := newResolver(index, graphs)
	for _, g := range graphs {
		for _, an := range g.anchored {
			if r.resolvesAll(an.anchors) {
				add(an.node)
			}
		}
	}
	// Anchored nodes can be link endpoints, so index them before any link.
	r.reindex()

	edges := []Edge{}
	for _, g := range graphs {
		for _, l := range g.links {
			src, tgt := r.resolve(l.Source), r.resolve(l.Target)
			if src == "" || tgt == "" {
				continue
			}
			id := l.ID
			if id == "" {
				id = l.IDPrefix + "::" + src + "→" + tgt
			}
			edges = append(edges, Edge{
				ID:           id,
				Source:       src,
				Target:       tgt,
				Type:         l.Type,
				Label:        l.Label,
				State:        l.State,
				SourceRegion: nodes[index[src]].Region,
				TargetRegion: nodes[index[tgt]].Region,
			})
		}
		edges = append(edges, g.edges...)
		for _, st := range g.stacks {
			if id := r.resolve(st.ref); id != "" {
				name := st.stackName
				nodes[index[id]].StackName = &name
			}
		}
	}

	regions := []string{}
	for _, n := range nodes {
		if !slices.Contains(regions, n.Region) {
			regions = append(regions, n.Region)
		}
	}
	if regionFilter != "" && !slices.Contains(regions, regionFilter) {
		regions = append(regions, regionFilter)
	}
	slices.Sort(regions)

	return Response{Regions: regions, Nodes: nodes, Edges: edges}
}

// resolver turns refs into the IDs of nodes Build kept.
type resolver struct {
	index   map[string]int
	aliases map[Ref]string
	// byName maps "<kind>::<name>" to every kept node ID with that suffix,
	// sorted, for AnyRegion fallback.
	byName map[string][]string
}

func newResolver(index map[string]int, graphs []*Graph) *resolver {
	r := &resolver{index: index, aliases: make(map[Ref]string), byName: make(map[string][]string)}
	for _, g := range graphs {
		for _, a := range g.aliases {
			if _, kept := index[a.nodeID]; !kept {
				continue
			}
			if _, taken := r.aliases[a.ref]; !taken {
				r.aliases[a.ref] = a.nodeID
			}
		}
	}
	r.reindex()
	return r
}

// reindex rebuilds byName from index.
func (r *resolver) reindex() {
	clear(r.byName)
	for id := range r.index {
		if _, name, ok := strings.Cut(id, "::"); ok {
			r.byName[name] = append(r.byName[name], id)
		}
	}
	for _, ids := range r.byName {
		slices.Sort(ids)
	}
}

func (r *resolver) resolve(ref Ref) string {
	switch ref.kind {
	case refNode:
		if _, ok := r.index[ref.key]; ok {
			return ref.key
		}
		if ref.anyRegion {
			if _, name, ok := strings.Cut(ref.key, "::"); ok {
				if ids := r.byName[name]; len(ids) > 0 {
					return ids[0]
				}
			}
		}
		return ""
	case refCFN, refImage:
		return r.aliases[ref]
	case refNone:
		return ""
	}
	return ""
}

func (r *resolver) resolvesAll(refs []Ref) bool {
	for _, ref := range refs {
		if r.resolve(ref) == "" {
			return false
		}
	}
	return true
}
