package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/freifunkMUC/freifunk-map-modern/internal/config"
	"github.com/freifunkMUC/freifunk-map-modern/internal/store"
)

const stateCacheFile = "federation_state.json"

// Store extends store.Store to manage multiple community data sources.
type Store struct {
	*store.Store
	client       *http.Client
	communities  []Community
	sources      []CommunitySource
	grafanaCache GrafanaCache
	nodeCommMap  map[string][]string
	fedMu        sync.RWMutex
}

func NewStore(cfg *config.Config) *Store {
	return &Store{
		Store: store.New(cfg),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		grafanaCache: make(GrafanaCache),
		nodeCommMap:  make(map[string][]string),
	}
}

// stateCache is the on-disk format for fast startup.
type stateCache struct {
	Communities []Community         `json:"communities"`
	Sources     []CommunitySource   `json:"sources"`
	NodeCommMap map[string][]string `json:"node_comm_map"`
	Snapshot    *snapshotCache      `json:"snapshot"`
	SavedAt     string              `json:"saved_at"`
}

type snapshotCache struct {
	Nodes []store.RawNode `json:"nodes"`
	Links []store.RawLink `json:"links"`
}

// RestoreState tries to load cached federation state from disk.
// Returns true if state was restored successfully.
func (fs *Store) RestoreState() bool {
	data, err := os.ReadFile(stateCacheFile)
	if err != nil {
		return false
	}

	var cache stateCache
	if err := json.Unmarshal(data, &cache); err != nil {
		log.Printf("Federation cache: corrupt, ignoring (%v)", err)
		return false
	}

	if len(cache.Communities) == 0 || len(cache.Sources) == 0 || cache.Snapshot == nil {
		return false
	}

	// Restore communities and sources
	fs.fedMu.Lock()
	fs.communities = cache.Communities
	fs.sources = cache.Sources
	fs.nodeCommMap = cache.NodeCommMap
	// Grafana cache is loaded separately by its own file
	fs.grafanaCache = LoadGrafanaCache()
	fs.fedMu.Unlock()

	// Rebuild the snapshot from cached raw data
	raw := &store.MeshviewerData{
		Timestamp: cache.SavedAt,
		Nodes:     cache.Snapshot.Nodes,
		Links:     cache.Snapshot.Links,
	}

	// Build domain names map
	communities := cache.Communities
	domainNames := make(map[string]string)
	for _, c := range communities {
		domainNames[c.Key] = c.Name
	}
	for k, v := range fs.Cfg.DomainNames {
		domainNames[k] = v
	}
	origDomains := fs.Cfg.DomainNames
	fs.Cfg.DomainNames = domainNames
	snap := fs.ProcessData(raw)
	fs.Cfg.DomainNames = origDomains

	// Re-apply community tags
	communityStats := make(map[string]int)
	for _, n := range snap.Nodes {
		comms := cache.NodeCommMap[n.NodeID]
		if len(comms) > 0 {
			n.Community = comms[0]
			n.Communities = comms
			for _, c := range comms {
				communityStats[c]++
			}
		}
	}
	snap.Stats.Communities = communityStats

	fs.SetSnapshot(snap)

	log.Printf("Federation cache: restored %d communities, %d sources, %d nodes (saved %s)",
		len(cache.Communities), len(cache.Sources), len(cache.Snapshot.Nodes), cache.SavedAt)
	return true
}

// SaveState persists the current federation state to disk for fast restart.
func (fs *Store) SaveState() {
	fs.fedMu.RLock()
	communities := fs.communities
	sources := fs.sources
	nodeCommMap := fs.nodeCommMap
	fs.fedMu.RUnlock()

	snap := fs.GetSnapshot()
	if snap == nil || len(snap.Nodes) == 0 {
		return
	}

	// Convert processed nodes back to raw format for compact storage
	rawNodes := make([]store.RawNode, 0, len(snap.NodeList))
	for _, n := range snap.NodeList {
		rn := store.RawNode{
			NodeID:      n.NodeID,
			Hostname:    n.Hostname,
			IsOnline:    store.FlexBool(n.IsOnline),
			IsGateway:   store.FlexBool(n.IsGateway),
			Clients:     n.Clients,
			ClientsW24:  n.ClientsW24,
			ClientsW5:   n.ClientsW5,
			ClientsOth:  n.ClientsOth,
			Domain:      n.Domain,
			MAC:         n.MAC,
			Owner:       n.Owner,
			Uptime:      n.Uptime,
			LoadAvg:     n.LoadAvg,
			MemoryUsage: n.MemUsage,
			RootfsUsage: n.RootfsUsage,
			Gateway:     n.Gateway,
			Lastseen:    n.Lastseen,
			Firstseen:   n.Firstseen,
			Nproc:       n.Nproc,
			Addresses:   n.Addresses,
			Model:       n.Model,
			Firmware: store.RawFirmware{
				Release:   n.Firmware,
				Base:      n.FWBase,
				ImageName: n.ImageName,
			},
			Autoupdater: store.RawAutoUpd{
				Enabled: store.FlexBool(n.Autoupdater),
				Branch:  n.Branch,
			},
		}
		if n.Lat != nil {
			rn.Location = &store.RawLocation{Latitude: *n.Lat, Longitude: *n.Lng}
		}
		rawNodes = append(rawNodes, rn)
	}

	rawLinks := make([]store.RawLink, 0, len(snap.Links))
	for _, l := range snap.Links {
		rawLinks = append(rawLinks, store.RawLink{
			Source: l.Source, Target: l.Target,
			SourceTQ: l.SourceTQ, TargetTQ: l.TargetTQ, Type: l.Type,
		})
	}

	cache := stateCache{
		Communities: communities,
		Sources:     sources,
		NodeCommMap: nodeCommMap,
		Snapshot:    &snapshotCache{Nodes: rawNodes, Links: rawLinks},
		SavedAt:     time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(cache)
	if err != nil {
		log.Printf("Federation cache: save error: %v", err)
		return
	}

	if err := os.WriteFile(stateCacheFile, data, 0644); err != nil {
		log.Printf("Federation cache: write error: %v", err)
		return
	}
	log.Printf("Federation cache: saved %d nodes, %d sources (%d bytes)",
		len(rawNodes), len(sources), len(data))
}

func (fs *Store) GetCommunities() []Community {
	fs.fedMu.RLock()
	defer fs.fedMu.RUnlock()
	return fs.communities
}

func (fs *Store) GetSources() []CommunitySource {
	fs.fedMu.RLock()
	defer fs.fedMu.RUnlock()
	return fs.sources
}

func (fs *Store) GetGrafanaCache() GrafanaCache {
	fs.fedMu.RLock()
	defer fs.fedMu.RUnlock()
	return fs.grafanaCache
}

// GrafanaInfoForNode returns the best Grafana info for a node.
// The returned string is the original node_id (without gateway community suffix).
func (fs *Store) GrafanaInfoForNode(nodeID string) (GrafanaInfo, string) {
	fs.fedMu.RLock()
	defer fs.fedMu.RUnlock()

	comms := fs.nodeCommMap[nodeID]

	originalID := nodeID
	for _, ck := range comms {
		suffix := "_" + ck
		if strings.HasSuffix(nodeID, suffix) {
			originalID = strings.TrimSuffix(nodeID, suffix)
			break
		}
	}

	var bestInfo GrafanaInfo
	for _, ck := range comms {
		if info, ok := fs.grafanaCache[ck]; ok {
			if info.DatasourceID > 0 {
				return info, originalID
			}
			if bestInfo.BaseURL == "" {
				bestInfo = info
			}
		}
	}
	return bestInfo, originalID
}

// DiscoverAndRefresh discovers communities and fetches all data.
func (fs *Store) DiscoverAndRefresh() error {
	log.Println("Federation: discovering communities from api.freifunk.net...")

	communities, err := DiscoverCommunities(fs.client)
	if err != nil {
		return fmt.Errorf("discovering communities: %w", err)
	}
	log.Printf("Federation: found %d communities with data URLs", len(communities))

	log.Println("Federation: probing data source URLs...")
	sources := ResolveBestSources(fs.client, communities, 50)
	log.Printf("Federation: %d communities have reachable data sources", len(sources))

	grafanaCache := DiscoverGrafanaURLs(fs.client, sources, communities)

	for _, c := range communities {
		if info, ok := grafanaCache[c.Key]; ok {
			for _, ak := range c.AllKeys {
				if _, exists := grafanaCache[ak]; !exists {
					grafanaCache[ak] = info
				}
			}
		}
	}
	for i := range sources {
		if sources[i].GrafanaURL == "" {
			if info, ok := grafanaCache[sources[i].CommunityKey]; ok {
				sources[i].GrafanaURL = info.BaseURL
			}
		}
	}
	for i := range communities {
		if communities[i].GrafanaURL == "" {
			if info, ok := grafanaCache[communities[i].Key]; ok {
				communities[i].GrafanaURL = info.BaseURL
			}
		}
	}

	// Add sources from dataPath discovery
	existingSources := make(map[string]bool)
	existingURLIdx := make(map[string]int)
	for i, s := range sources {
		existingSources[s.CommunityKey] = true
		existingURLIdx[s.DataURL] = i
	}
	for _, c := range communities {
		if existingSources[c.Key] {
			continue
		}
		if info, ok := grafanaCache[c.Key]; ok && len(info.DataPaths) > 0 {
			for _, dp := range info.DataPaths {
				// Skip relative paths from stale cache entries
				if !strings.HasPrefix(dp, "http://") && !strings.HasPrefix(dp, "https://") {
					continue
				}
				if idx, exists := existingURLIdx[dp]; exists {
					for _, ak := range c.AllKeys {
						sources[idx].CommunityKeys = store.AppendUnique(sources[idx].CommunityKeys, ak)
					}
					existingSources[c.Key] = true
					log.Printf("Federation: tagged %s keys onto existing source %s (via %s)", c.Key, sources[idx].CommunityKey, dp)
					continue
				}
				sources = append(sources, CommunitySource{
					CommunityKey:  c.Key,
					CommunityKeys: c.AllKeys,
					DataURL:       dp,
					DataType:      "meshviewer",
					GrafanaURL:    info.BaseURL,
					MapURLs:       c.MeshviewerURLs,
				})
				existingURLIdx[dp] = len(sources) - 1
				existingSources[c.Key] = true
				log.Printf("Federation: added dataPath source for %s: %s", c.Key, dp)
			}
		}
	}

	fs.fedMu.Lock()
	fs.communities = communities
	fs.sources = sources
	fs.grafanaCache = grafanaCache
	fs.fedMu.Unlock()

	return fs.RefreshAllSources()
}

// RefreshAllSources fetches node data from all discovered sources and merges.
func (fs *Store) RefreshAllSources() error {
	sources := fs.GetSources()
	if len(sources) == 0 {
		return fmt.Errorf("no data sources available")
	}

	type fetchResult struct {
		communityKey string
		source       CommunitySource
		data         *store.MeshviewerData
		err          error
	}

	ch := make(chan fetchResult, len(sources))
	sem := make(chan struct{}, 50)
	var wg sync.WaitGroup

	for _, src := range sources {
		wg.Add(1)
		go func(src CommunitySource) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			data, err := fs.fetchSource(src)
			ch <- fetchResult{
				communityKey: src.CommunityKey,
				source:       src,
				data:         data,
				err:          err,
			}
		}(src)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	merged := &store.MeshviewerData{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	nodeCommMap := make(map[string][]string)
	seenNodes := make(map[string]bool)
	seenLinks := make(map[string]bool)

	successCount := 0
	failCount := 0
	for r := range ch {
		if r.err != nil {
			failCount++
			continue
		}
		if r.data == nil {
			continue
		}

		allComms := r.source.CommunityKeys
		if len(allComms) == 0 {
			allComms = []string{r.communityKey}
		}

		// Suffix gateway node_ids with community key
		gwRename := make(map[string]string)
		for i := range r.data.Nodes {
			if bool(r.data.Nodes[i].IsGateway) && r.data.Nodes[i].NodeID != "" {
				orig := r.data.Nodes[i].NodeID
				suffixed := orig + "_" + r.communityKey
				gwRename[orig] = suffixed
				r.data.Nodes[i].NodeID = suffixed
			}
		}

		for i := range r.data.Nodes {
			if newGW, ok := gwRename[r.data.Nodes[i].Gateway]; ok {
				r.data.Nodes[i].Gateway = newGW
			}
		}
		for i := range r.data.Links {
			if newID, ok := gwRename[r.data.Links[i].Source]; ok {
				r.data.Links[i].Source = newID
			}
			if newID, ok := gwRename[r.data.Links[i].Target]; ok {
				r.data.Links[i].Target = newID
			}
		}

		for i := range r.data.Nodes {
			nid := r.data.Nodes[i].NodeID
			if nid == "" {
				continue
			}
			if r.data.Nodes[i].Domain == "" {
				r.data.Nodes[i].Domain = r.communityKey
			}

			if seenNodes[nid] {
				for _, ck := range allComms {
					nodeCommMap[nid] = store.AppendUnique(nodeCommMap[nid], ck)
				}
			} else {
				seenNodes[nid] = true
				for _, ck := range allComms {
					nodeCommMap[nid] = store.AppendUnique(nodeCommMap[nid], ck)
				}
				merged.Nodes = append(merged.Nodes, r.data.Nodes[i])
			}
		}

		for i := range r.data.Links {
			lk := r.data.Links[i].Source + ">" + r.data.Links[i].Target
			if !seenLinks[lk] {
				seenLinks[lk] = true
				merged.Links = append(merged.Links, r.data.Links[i])
			}
		}

		successCount++
	}

	log.Printf("Federation: merged data from %d/%d sources (%d failed, %d unique nodes, %d links)",
		successCount, len(sources), failCount, len(merged.Nodes), len(merged.Links))

	communities := fs.GetCommunities()
	domainNames := make(map[string]string)
	for _, c := range communities {
		domainNames[c.Key] = c.Name
	}
	for k, v := range fs.Cfg.DomainNames {
		domainNames[k] = v
	}
	origDomains := fs.Cfg.DomainNames
	fs.Cfg.DomainNames = domainNames
	snap := fs.ProcessData(merged)
	fs.Cfg.DomainNames = origDomains

	communityStats := make(map[string]int)
	for _, n := range snap.Nodes {
		comms := nodeCommMap[n.NodeID]
		if len(comms) > 0 {
			n.Community = comms[0]
			n.Communities = comms
			for _, c := range comms {
				communityStats[c]++
			}
		}
	}
	snap.Stats.Communities = communityStats

	fs.fedMu.Lock()
	fs.nodeCommMap = nodeCommMap
	fs.fedMu.Unlock()

	fs.SetSnapshot(snap)

	// Persist state for fast restart
	fs.SaveState()

	return nil
}

func (fs *Store) fetchSource(src CommunitySource) (*store.MeshviewerData, error) {
	resp, err := fs.client.Get(src.DataURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	switch src.DataType {
	case "meshviewer":
		var mv store.MeshviewerData
		if err := json.Unmarshal(body, &mv); err != nil {
			return nil, fmt.Errorf("parsing meshviewer JSON: %w", err)
		}
		return &mv, nil

	case "nodelist":
		mv, err := ParseNodelistToMeshviewer(body)
		if err != nil {
			return nil, fmt.Errorf("parsing nodelist JSON: %w", err)
		}
		return mv, nil

	default:
		return nil, fmt.Errorf("unknown data type: %s", src.DataType)
	}
}

// RunRefreshLoop periodically re-discovers communities and refreshes data.
func (fs *Store) RunRefreshLoop(ctx context.Context, hub store.SSEBroadcaster) {
	discoveryTicker := time.NewTicker(30 * time.Minute)
	dataTicker := time.NewTicker(fs.Cfg.RefreshDuration)
	defer discoveryTicker.Stop()
	defer dataTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-discoveryTicker.C:
			old := fs.GetSnapshot()
			if err := fs.DiscoverAndRefresh(); err != nil {
				log.Printf("Federation discovery error: %v", err)
				continue
			}
			snap := fs.GetSnapshot()
			log.Printf("Federation re-discovery: %d nodes (%d online), %d clients, %d SSE clients",
				snap.Stats.TotalNodes, snap.Stats.OnlineNodes, snap.Stats.TotalClients, hub.ClientCount())
			diff := store.ComputeDiff(old, snap)
			if diff != nil {
				hub.Broadcast(diff)
			}

		case <-dataTicker.C:
			old := fs.GetSnapshot()
			if err := fs.RefreshAllSources(); err != nil {
				log.Printf("Federation data refresh error: %v", err)
				continue
			}
			snap := fs.GetSnapshot()
			log.Printf("Federation data refreshed: %d nodes (%d online), %d clients, %d SSE clients",
				snap.Stats.TotalNodes, snap.Stats.OnlineNodes, snap.Stats.TotalClients, hub.ClientCount())
			diff := store.ComputeDiff(old, snap)
			if diff != nil {
				hub.Broadcast(diff)
			}
		}
	}
}
