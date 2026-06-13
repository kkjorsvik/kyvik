package handlers

import (
	"net/http"

	"github.com/kkjorsvik/kyvik/internal/cluster"
)

// SetClusterManager sets the cluster manager on the handlers, enabling cluster management pages.
func (h *Handlers) SetClusterManager(m cluster.Manager) {
	h.clusterMgr = m
}

// ClusterList renders the cluster management page showing all nodes.
func (h *Handlers) ClusterList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if h.clusterMgr == nil {
		http.Error(w, "clustering is not enabled", http.StatusNotFound)
		return
	}

	nodes, err := h.clusterMgr.ListNodes()
	if err != nil {
		h.serverError(w, r, "listing nodes", err)
		return
	}

	// Tag leader node from the manager perspective.
	currentNodeID := h.clusterMgr.NodeID()

	data := map[string]any{
		"Nav":           "cluster",
		"Title":         "Cluster",
		"Nodes":         nodes,
		"CurrentNodeID": currentNodeID,
		"IsLeader":      h.clusterMgr.IsLeader(),
		"NodeCount":     len(nodes),
	}

	if isHTMX(r) {
		h.injectTemplateUser(ctx, data)
		h.renderFragment(w, r, "cluster-list", data)
		return
	}

	h.renderPageWithRequest(w, r, "cluster-list", data)
}

// ClusterMigrateAgent triggers reassignment of a single agent.
func (h *Handlers) ClusterMigrateAgent(w http.ResponseWriter, r *http.Request) {
	if h.clusterMgr == nil {
		http.Error(w, "clustering is not enabled", http.StatusNotFound)
		return
	}

	agentID := r.PathValue("id")
	if agentID == "" {
		http.Error(w, "missing agent ID", http.StatusBadRequest)
		return
	}

	if _, err := h.clusterMgr.RequestAssignment(agentID); err != nil {
		h.serverError(w, r, "migrating agent", err)
		return
	}

	http.Redirect(w, r, "/agents", http.StatusSeeOther)
}

// ClusterDrain triggers a drain operation on a node, reassigning its agents.
func (h *Handlers) ClusterDrain(w http.ResponseWriter, r *http.Request) {
	if h.clusterMgr == nil {
		http.Error(w, "clustering is not enabled", http.StatusNotFound)
		return
	}

	nodeID := r.PathValue("nodeID")
	if nodeID == "" {
		http.Error(w, "missing node ID", http.StatusBadRequest)
		return
	}

	if err := h.clusterMgr.DrainNode(nodeID); err != nil {
		h.serverError(w, r, "draining node", err)
		return
	}

	http.Redirect(w, r, "/cluster", http.StatusSeeOther)
}
