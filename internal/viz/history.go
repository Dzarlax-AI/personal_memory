package viz

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/memory/maintenance"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/go-chi/chi/v5"
)

const (
	hardHistoryFetchedNodes = 32
	hardHistoryQueuedIDs    = 32
	hardHistoryLinks        = 256
)

type historyLink struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

type historyResponse struct {
	Nodes     []factSummary `json:"nodes"`
	Links     []historyLink `json:"links"`
	Truncated bool          `json:"truncated"`
}

type historyQueueItem struct {
	id string
}

type historyRelation struct {
	link     historyLink
	targetID string
}

func (h *Handler) apiFactHistory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "missing point id", http.StatusBadRequest)
		return
	}
	timeout := h.historyTimeout
	if timeout <= 0 {
		timeout = vizComputationTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	response, found, err := h.loadFactHistory(ctx, id)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, "history request was canceled", http.StatusRequestTimeout)
			return
		}
		writeInternalError(w, "unable to load fact history")
		return
	}
	if !found {
		http.Error(w, "fact not found", http.StatusNotFound)
		return
	}
	writeJSON(w, response)
}

func (h *Handler) loadFactHistory(ctx context.Context, rootID string) (historyResponse, bool, error) {
	response := historyResponse{Nodes: []factSummary{}, Links: []historyLink{}}
	queue := []historyQueueItem{{id: rootID}}
	discovered := map[string]struct{}{rootID: {}}
	statuses := make(map[string]string)
	pendingLinks := make(map[string][]int)
	adjacency := make(map[string][]string)
	linkIndexes := make(map[string]int)
	fetched := 0

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return historyResponse{}, false, err
		}
		item := queue[0]
		queue = queue[1:]
		if fetched >= hardHistoryFetchedNodes {
			response.Truncated = true
			break
		}
		point, found, err := h.qdrant.Get(ctx, item.id)
		fetched++
		if err != nil {
			return historyResponse{}, false, err
		}
		if !found {
			if item.id == rootID {
				return historyResponse{}, false, nil
			}
			statuses[item.id] = "missing"
			resolvePendingHistoryLinks(&response, pendingLinks, item.id, "missing")
			continue
		}
		if !maintenance.IsActive(point.Payload) {
			if item.id == rootID {
				return historyResponse{}, false, nil
			}
			statuses[item.id] = "unavailable"
			resolvePendingHistoryLinks(&response, pendingLinks, item.id, "unavailable")
			continue
		}
		statuses[item.id] = "resolved"
		resolvePendingHistoryLinks(&response, pendingLinks, item.id, "resolved")
		view, _ := lifecycle.Parse(point.Payload, point.ID)
		response.Nodes = append(response.Nodes, historyPointToSummary(point, view))
		if !view.Valid {
			continue
		}

		if len(linkIndexes) >= hardHistoryLinks {
			if len(view.Supersedes) > 0 || len(view.SupersededBy) > 0 {
				response.Truncated = true
			}
			continue
		}
		// Normalize before applying any processing, queue, or response cap so a
		// payload permutation cannot change which logical relationships survive.
		supersedes := sortedUniqueIDs(view.Supersedes)
		supersededBy := sortedUniqueIDs(view.SupersededBy)
		relations := make([]historyRelation, 0, len(supersedes)+len(supersededBy))
		for _, target := range supersedes {
			relations = append(relations, historyRelation{
				link: historyLink{From: point.ID, To: target, Type: "supersedes"}, targetID: target,
			})
		}
		for _, target := range supersededBy {
			// A.superseded_by=[B] and B.supersedes=[A] describe the same
			// directed logical edge B -> A. Canonicalizing here prevents the
			// mirrored storage representation from looking like a cycle.
			relations = append(relations, historyRelation{
				link: historyLink{From: target, To: point.ID, Type: "supersedes"}, targetID: target,
			})
		}
		sort.Slice(relations, func(i, j int) bool {
			return historyLinkLess(relations[i].link, relations[j].link)
		})
		for _, relation := range relations {
			link := relation.link
			identity := historyLinkIdentity(link)
			if _, exists := linkIndexes[identity]; exists {
				continue
			}
			// Mirrored supersedes/superseded_by metadata describes one logical
			// edge. Charge the response budget only after canonical deduplication.
			if len(linkIndexes) >= hardHistoryLinks {
				response.Truncated = true
				break
			}
			if link.From == link.To || historyPathExists(adjacency, link.To, link.From) {
				link.Status = "cycle"
				response.Links = append(response.Links, link)
				linkIndexes[identity] = len(response.Links) - 1
				continue
			}
			adjacency[link.From] = append(adjacency[link.From], link.To)
			if status := statuses[relation.targetID]; status != "" {
				link.Status = status
				response.Links = append(response.Links, link)
				linkIndexes[identity] = len(response.Links) - 1
				continue
			}
			if _, exists := discovered[relation.targetID]; exists {
				response.Links = append(response.Links, link)
				linkIndexes[identity] = len(response.Links) - 1
				pendingLinks[relation.targetID] = append(pendingLinks[relation.targetID], len(response.Links)-1)
				continue
			}
			if len(discovered) >= hardHistoryQueuedIDs || fetched+len(queue) >= hardHistoryFetchedNodes {
				link.Status = "unavailable"
				response.Links = append(response.Links, link)
				linkIndexes[identity] = len(response.Links) - 1
				response.Truncated = true
				continue
			}
			discovered[relation.targetID] = struct{}{}
			response.Links = append(response.Links, link)
			linkIndexes[identity] = len(response.Links) - 1
			pendingLinks[relation.targetID] = append(pendingLinks[relation.targetID], len(response.Links)-1)
			queue = append(queue, historyQueueItem{id: relation.targetID})
		}
	}

	// Root first is useful to clients. Remaining nodes and all links have stable
	// lexical order independent of backend response timing and payload ordering.
	if len(response.Nodes) > 1 {
		sort.Slice(response.Nodes[1:], func(i, j int) bool {
			return response.Nodes[i+1].ID < response.Nodes[j+1].ID
		})
	}
	sortHistoryLinks(response.Links)
	return response, true, nil
}

func resolvePendingHistoryLinks(response *historyResponse, pending map[string][]int, id, status string) {
	for _, index := range pending[id] {
		response.Links[index].Status = status
	}
	delete(pending, id)
}

func historyPathExists(adjacency map[string][]string, from, to string) bool {
	queue := []string{from}
	seen := map[string]struct{}{from: {}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == to {
			return true
		}
		for _, next := range adjacency[current] {
			if _, exists := seen[next]; exists {
				continue
			}
			seen[next] = struct{}{}
			queue = append(queue, next)
		}
	}
	return false
}

func historyPointToSummary(point qdrant.Point, view lifecycle.View) factSummary {
	summary := pointToSummary(qdrant.ScrollPoint{ID: point.ID, Payload: point.Payload})
	summary.Lifecycle = lifecycleSummaryDTO(view)
	// History links are the bounded, canonical relationship representation.
	// Do not duplicate storage relationship arrays inside every node and defeat
	// the response-level hardHistoryLinks bound.
	summary.Lifecycle.Supersedes = []string{}
	summary.Lifecycle.SupersededBy = []string{}
	return summary
}

func sortHistoryLinks(links []historyLink) {
	sort.SliceStable(links, func(i, j int) bool {
		return historyLinkLess(links[i], links[j])
	})
}

func historyLinkLess(a, b historyLink) bool {
	if a.From != b.From {
		return a.From < b.From
	}
	if a.To != b.To {
		return a.To < b.To
	}
	if a.Type != b.Type {
		return a.Type < b.Type
	}
	return a.Status < b.Status
}

func historyLinkIdentity(link historyLink) string {
	return link.From + "\x00" + link.To + "\x00" + link.Type
}
