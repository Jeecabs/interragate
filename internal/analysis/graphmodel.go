package analysis

import (
	"sort"

	"github.com/Jeecabs/interragate/internal/kb"
	"github.com/Jeecabs/interragate/internal/model"
)

// graphModel converts the dependency graph into the report's form, one node
// per configuration resource with its instances' actions rolled up.
func (a *analyzer) graphModel(r *model.Report) *model.Graph {
	g := &model.Graph{}
	nodes := map[string]*model.GraphNode{}
	get := func(id, module, mode, typ, name string) *model.GraphNode {
		if n := nodes[id]; n != nil {
			return n
		}
		info := kb.Lookup(typ)
		n := &model.GraphNode{ID: id, Module: module, Mode: mode, Type: typ, Name: name, Noun: info.Noun,
			Category: string(info.Category), Action: model.NoOp, Actions: map[model.Action]int{}}
		nodes[id] = n
		return n
	}
	for _, ch := range r.Changes {
		n := get(ch.Resource, moduleConfig(ch.Module), ch.Mode, ch.Type, ch.Name)
		action := ch.Action
		if action == model.NoOp && ch.Moved {
			action = "move"
		}
		n.Actions[action]++
		n.Instances = append(n.Instances, ch.Address)
		if ch.Action.Rank() > n.Action.Rank() {
			n.Action = ch.Action
		}
		if ch.Severity > n.Severity {
			n.Severity = ch.Severity
		}
		if n.Location == nil && ch.Location != nil {
			n.Location = ch.Location
		}
	}
	for id, gn := range a.graph.Nodes {
		get(id, gn.Module, gn.Mode, gn.Type, gn.Name)
	}
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		g.Nodes = append(g.Nodes, nodes[id])
		if gn := a.graph.Nodes[id]; gn != nil {
			for _, up := range gn.Up {
				if nodes[up] != nil {
					g.Edges = append(g.Edges, [2]string{up, id})
				}
			}
		}
	}
	return g
}
