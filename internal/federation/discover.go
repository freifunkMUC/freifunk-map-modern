package federation

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/freifunkMUC/freifunk-map-modern/internal/store"
	"github.com/freifunkMUC/freifunk-map-modern/internal/urlcheck"
)

const FFDirectoryURL = "https://api.freifunk.net/data/ffSummarizedDir.json"

// Community represents a discovered Freifunk community.
type Community struct {
	Key            string   `json:"key"`
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Lat            float64  `json:"lat,omitempty"`
	Lng            float64  `json:"lng,omitempty"`
	Nodes          int      `json:"nodes,omitempty"`
	MeshviewerURLs []string `json:"meshviewer_urls,omitempty"`
	NodelistURLs   []string `json:"nodelist_urls,omitempty"`
	GrafanaURL     string   `json:"grafana_url,omitempty"`
	Metacommunity  string   `json:"metacommunity,omitempty"`
	AllKeys        []string `json:"all_keys,omitempty"`
	HasError       bool     `json:"has_error,omitempty"`
	LastChanged    string   `json:"last_changed,omitempty"`
}

// CommunitySource is a resolved data source for node data.
type CommunitySource struct {
	CommunityKey  string
	CommunityKeys []string
	DataURL       string
	DataType      string // "meshviewer" or "nodelist"
	GrafanaURL    string
	MapURLs       []string
}

// --- Freifunk API JSON structures ---

type ffAPIEntry struct {
	Name          string      `json:"name"`
	URL           string      `json:"url"`
	Metacommunity string      `json:"metacommunity"`
	Location      *ffLocation `json:"location"`
	State         *ffState    `json:"state"`
	NodeMaps      []ffNodeMap `json:"nodeMaps"`
	Services      []ffService `json:"services"`
	Error         string      `json:"error"`
}

type ffLocation struct {
	City    string     `json:"city"`
	Country string     `json:"country"`
	Lat     float64    `json:"lat"`
	Lon     float64    `json:"lon"`
	GeoCode *ffGeoCode `json:"geoCode"`
}

type ffGeoCode struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type ffState struct {
	Nodes      int         `json:"nodes"`
	LastChange interface{} `json:"lastchange"`
	Focus      []string    `json:"focus"`
}

type ffNodeMap struct {
	URL           string      `json:"url"`
	Interval      interface{} `json:"interval"`
	TechnicalType string      `json:"technicalType"`
	MapType       string      `json:"mapType"`
}

type ffService struct {
	ServiceName string `json:"serviceName"`
	ExternalURI string `json:"externalUri"`
	InternalURI string `json:"internalUri"`
}

// --- Nodelist.json format ---

type NodelistData struct {
	Version   interface{}    `json:"version"`
	Nodes     []NodelistNode `json:"nodes"`
	UpdatedAt string         `json:"updated_at"`
}

type NodelistNode struct {
	ID       interface{}       `json:"id"`
	Name     string            `json:"name"`
	Status   NodelistStatus    `json:"status"`
	Position *NodelistPosition `json:"position"`
}

type NodelistStatus struct {
	Online      interface{} `json:"online"`
	Lastcontact interface{} `json:"lastcontact"`
	Clients     interface{} `json:"clients"`
}

type NodelistPosition struct {
	Lat  interface{} `json:"lat"`
	Long interface{} `json:"long"`
	Lon  interface{} `json:"lon"`
}

// DiscoverCommunities fetches the Freifunk API directory.
func DiscoverCommunities(client *http.Client) ([]Community, error) {
	req, err := http.NewRequest("GET", FFDirectoryURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "freifunk-map-modern/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching freifunk directory: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("freifunk directory returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("reading directory body: %w", err)
	}

	var directory map[string]ffAPIEntry
	if err := json.Unmarshal(body, &directory); err != nil {
		return nil, fmt.Errorf("parsing directory JSON: %w", err)
	}

	var communities []Community

	for key, entry := range directory {
		c := Community{
			Key:           key,
			Name:          entry.Name,
			URL:           entry.URL,
			Metacommunity: entry.Metacommunity,
		}

		if entry.Location != nil {
			if entry.Location.Lat != 0 {
				c.Lat = entry.Location.Lat
				c.Lng = entry.Location.Lon
			} else if entry.Location.GeoCode != nil {
				c.Lat = entry.Location.GeoCode.Lat
				c.Lng = entry.Location.GeoCode.Lon
			}
		}

		if entry.State != nil {
			c.Nodes = entry.State.Nodes
			if s, ok := entry.State.LastChange.(string); ok {
				c.LastChanged = s
			}
		}

		for _, nm := range entry.NodeMaps {
			u := strings.TrimSpace(nm.URL)
			if u == "" {
				continue
			}
			if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				u = "https://" + u
			}

			tt := strings.ToLower(nm.TechnicalType)

			if (tt == "meshviewer" || tt == "hopglass" || tt == "ffmap") && strings.HasSuffix(u, ".json") {
				// Direct .json URL — could be meshviewer.json or nodes.json format.
				// We accept both and auto-detect format at fetch time.
				c.MeshviewerURLs = append(c.MeshviewerURLs, u)
			} else if tt == "nodelist" && strings.HasSuffix(u, ".json") {
				c.NodelistURLs = append(c.NodelistURLs, u)
			} else if tt == "nodelist" {
				c.NodelistURLs = append(c.NodelistURLs, u)
			} else if tt == "meshviewer" || tt == "ffmap" || tt == "hopglass" {
				base := strings.TrimSuffix(u, "/")
				c.MeshviewerURLs = append(c.MeshviewerURLs, base+"/data/meshviewer.json")
				c.MeshviewerURLs = append(c.MeshviewerURLs, base+"/meshviewer.json")
				c.MeshviewerURLs = append(c.MeshviewerURLs, base+"/data/nodes.json")
				c.MeshviewerURLs = append(c.MeshviewerURLs, base+"/nodes.json")
				if parsed, err := url.Parse(u); err == nil {
					rootData := parsed.Scheme + "://" + parsed.Host + "/data/meshviewer.json"
					if rootData != base+"/data/meshviewer.json" {
						c.MeshviewerURLs = append(c.MeshviewerURLs, rootData)
					}
				}
			}
		}

		for _, svc := range entry.Services {
			name := strings.ToLower(svc.ServiceName)
			if strings.Contains(name, "grafana") || strings.Contains(name, "stats") {
				uri := svc.ExternalURI
				if uri == "" {
					uri = svc.InternalURI
				}
				if uri != "" && (strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://")) {
					c.GrafanaURL = uri
					break
				}
			}
		}

		if len(c.MeshviewerURLs) > 0 || len(c.NodelistURLs) > 0 {
			communities = append(communities, c)
		}
	}

	// Consolidate communities with the same name
	nameMap := make(map[string]*Community)
	nameIdx := make(map[string]int) // name -> index in mergedComms
	var mergedComms []Community
	for i := range communities {
		c := &communities[i]
		existing, ok := nameMap[c.Name]
		if !ok {
			merged := *c
			merged.AllKeys = []string{c.Key}
			nameMap[c.Name] = &merged
			nameIdx[c.Name] = len(mergedComms)
			mergedComms = append(mergedComms, merged)
			continue
		}
		existing.AllKeys = append(existing.AllKeys, c.Key)
		// Use the sub-community with the most nodes as the primary key,
		// breaking ties alphabetically for deterministic results.
		if c.Nodes > existing.Nodes || (c.Nodes == existing.Nodes && c.Key < existing.Key) {
			existing.Key = c.Key
			existing.Nodes = c.Nodes
			existing.Lat = c.Lat
			existing.Lng = c.Lng
		}
		for _, u := range c.MeshviewerURLs {
			if !containsStr(existing.MeshviewerURLs, u) {
				existing.MeshviewerURLs = append(existing.MeshviewerURLs, u)
			}
		}
		for _, u := range c.NodelistURLs {
			if !containsStr(existing.NodelistURLs, u) {
				existing.NodelistURLs = append(existing.NodelistURLs, u)
			}
		}
		if c.GrafanaURL != "" && existing.GrafanaURL == "" {
			existing.GrafanaURL = c.GrafanaURL
		}
		if c.Metacommunity != "" && existing.Metacommunity == "" {
			existing.Metacommunity = c.Metacommunity
		}
		mergedComms[nameIdx[c.Name]] = *existing
	}
	communities = mergedComms

	log.Printf("Federation: consolidated to %d unique communities", len(communities))

	sort.Slice(communities, func(i, j int) bool {
		return communities[i].Nodes > communities[j].Nodes
	})

	return communities, nil
}

// ResolveBestSources picks the best data source for each community.
func ResolveBestSources(client *http.Client, communities []Community, maxConcurrency int) []CommunitySource {
	type result struct {
		source CommunitySource
		ok     bool
	}

	// Shared probe client with generous timeout and connection pooling
	probeClient := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        200,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	// Buffer generously — communities can produce multiple sources
	ch := make(chan result, len(communities)*3)
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup

	for _, c := range communities {
		wg.Add(1)
		go func(c Community) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			mapURLs := CollectMapBases(c)
			found := false

			// Track hosts that timed out — skip other URLs on the same host.
			deadHosts := make(map[string]bool)
			probe := func(u string) bool {
				if parsed, err := url.Parse(u); err == nil {
					if deadHosts[parsed.Hostname()] {
						return false
					}
				}
				ok, deadHost := ProbeURL(probeClient, u)
				if deadHost != "" {
					deadHosts[deadHost] = true
				}
				return ok
			}

			// Probe ALL meshviewer URLs — communities may have multiple
			// distinct data sources (e.g. different domains/subpaths)
			for _, u := range c.MeshviewerURLs {
				if probe(u) {
					dtype := "meshviewer"
					if strings.HasSuffix(u, "/nodes.json") {
						dtype = "nodes"
					}
					ch <- result{source: CommunitySource{
						CommunityKey: c.Key, CommunityKeys: c.AllKeys,
						DataURL: u, DataType: dtype,
						GrafanaURL: c.GrafanaURL, MapURLs: mapURLs,
					}, ok: true}
					found = true
				}
			}

			// Only try nodelists if no meshviewer source worked
			if !found {
				for _, u := range c.NodelistURLs {
					if probe(u) {
						ch <- result{source: CommunitySource{
							CommunityKey: c.Key, CommunityKeys: c.AllKeys,
							DataURL: u, DataType: "nodelist",
							GrafanaURL: c.GrafanaURL, MapURLs: mapURLs,
						}, ok: true}
						found = true
						break // one nodelist is enough
					}
				}
			}

			// For nodelist-only communities, also try to find a richer source
			// (meshviewer.json or nodes.json) at the same base directory.
			if found && len(c.MeshviewerURLs) == 0 {
				tried := make(map[string]bool)
				for _, u := range c.NodelistURLs {
					base := u
					if idx := strings.LastIndex(base, "/"); idx > 0 {
						base = base[:idx]
					}
					for _, candidate := range []struct {
						url   string
						dtype string
					}{
						{base + "/meshviewer.json", "meshviewer"},
						{base + "/nodes.json", "nodes"},
					} {
						if !tried[candidate.url] {
							tried[candidate.url] = true
							if probe(candidate.url) {
								ch <- result{source: CommunitySource{
									CommunityKey: c.Key, CommunityKeys: c.AllKeys,
									DataURL: candidate.url, DataType: candidate.dtype,
									GrafanaURL: c.GrafanaURL, MapURLs: mapURLs,
								}, ok: true}
								break
							}
						}
					}
				}
			}

			// Last resort: derive meshviewer/nodes.json URLs from nodelist base paths
			if !found {
				tried := make(map[string]bool)
				for _, u := range c.MeshviewerURLs {
					tried[u] = true
				}
				for _, u := range c.NodelistURLs {
					base := u
					if idx := strings.LastIndex(base, "/"); idx > 0 {
						base = base[:idx]
					}
					// Try meshviewer.json at the nodelist's directory
					mvURL := base + "/meshviewer.json"
					if !tried[mvURL] {
						tried[mvURL] = true
						if probe(mvURL) {
							ch <- result{source: CommunitySource{
								CommunityKey: c.Key, CommunityKeys: c.AllKeys,
								DataURL: mvURL, DataType: "meshviewer",
								GrafanaURL: c.GrafanaURL, MapURLs: mapURLs,
							}, ok: true}
							found = true
							break
						}
					}
					// Try nodes.json at the nodelist's directory
					nodesURL := base + "/nodes.json"
					if !tried[nodesURL] {
						tried[nodesURL] = true
						if probe(nodesURL) {
							ch <- result{source: CommunitySource{
								CommunityKey: c.Key, CommunityKeys: c.AllKeys,
								DataURL: nodesURL, DataType: "nodes",
								GrafanaURL: c.GrafanaURL, MapURLs: mapURLs,
							}, ok: true}
							found = true
							break
						}
					}
				}
			}

			if !found {
				ch <- result{ok: false}
			}
		}(c)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var sources []CommunitySource
	for r := range ch {
		if r.ok {
			sources = append(sources, r.source)
		}
	}

	// De-duplicate by data URL
	urlCommunities := make(map[string][]string)
	for _, src := range sources {
		for _, ck := range src.CommunityKeys {
			urlCommunities[src.DataURL] = store.AppendUnique(urlCommunities[src.DataURL], ck)
		}
		urlCommunities[src.DataURL] = store.AppendUnique(urlCommunities[src.DataURL], src.CommunityKey)
	}
	seen := make(map[string]bool)
	deduped := make([]CommunitySource, 0, len(sources))
	for _, src := range sources {
		if !seen[src.DataURL] {
			seen[src.DataURL] = true
			src.CommunityKeys = urlCommunities[src.DataURL]
			deduped = append(deduped, src)
		}
	}

	return deduped
}

// ParseNodelistToMeshviewer converts nodelist.json to MeshviewerData.
func ParseNodelistToMeshviewer(data []byte) (*store.MeshviewerData, error) {
	var nl NodelistData
	if err := json.Unmarshal(data, &nl); err != nil {
		return nil, err
	}

	mv := &store.MeshviewerData{
		Timestamp: nl.UpdatedAt,
		Nodes:     make([]store.RawNode, 0, len(nl.Nodes)),
	}

	for _, n := range nl.Nodes {
		nodeID := ifaceToString(n.ID)
		if nodeID == "" {
			continue
		}
		rn := store.RawNode{
			NodeID:   nodeID,
			Hostname: n.Name,
			IsOnline: store.FlexBool(ifaceToBool(n.Status.Online)),
			Clients:  store.FlexInt(ifaceToInt(n.Status.Clients)),
			Lastseen: ifaceToString(n.Status.Lastcontact),
			MAC:      nodeID,
		}

		if n.Position != nil {
			lat := ifaceToFloat(n.Position.Lat)
			lng := ifaceToFloat(n.Position.Long)
			if lng == 0 {
				lng = ifaceToFloat(n.Position.Lon)
			}
			if lat != 0 || lng != 0 {
				rn.Location = &store.RawLocation{
					Latitude:  lat,
					Longitude: lng,
				}
			}
		}

		mv.Nodes = append(mv.Nodes, rn)
	}

	return mv, nil
}

// --- nodes.json format (Yanic/hopglass output) ---

type NodesJSONData struct {
	Version   interface{}     `json:"version"`
	Timestamp string          `json:"timestamp"`
	Nodes     []NodesJSONNode `json:"nodes"`
}

type NodesJSONNode struct {
	Firstseen  string              `json:"firstseen"`
	Lastseen   string              `json:"lastseen"`
	Flags      NodesJSONFlags      `json:"flags"`
	Statistics NodesJSONStatistics `json:"statistics"`
	Nodeinfo   NodesJSONNodeinfo   `json:"nodeinfo"`
}

type NodesJSONFlags struct {
	Online  bool `json:"online"`
	Gateway bool `json:"gateway"`
}

type NodesJSONStatistics struct {
	NodeID      string      `json:"node_id"`
	Clients     interface{} `json:"clients"`
	RootfsUsage interface{} `json:"rootfs_usage"`
	LoadAvg     interface{} `json:"loadavg"`
	MemoryUsage interface{} `json:"memory_usage"`
	Uptime      interface{} `json:"uptime"`
	Gateway     string      `json:"gateway"`
	Gateway6    string      `json:"gateway6"`
	Processes   interface{} `json:"processes"`
}

type NodesJSONNodeinfo struct {
	NodeID   string             `json:"node_id"`
	Hostname string             `json:"hostname"`
	Network  NodesJSONNetwork   `json:"network"`
	Owner    *NodesJSONOwner    `json:"owner"`
	System   NodesJSONSystem    `json:"system"`
	Location *NodesJSONLocation `json:"location"`
	Software NodesJSONSoftware  `json:"software"`
	Hardware NodesJSONHardware  `json:"hardware"`
	VPN      bool               `json:"vpn"`
}

type NodesJSONNetwork struct {
	MAC       string   `json:"mac"`
	Addresses []string `json:"addresses"`
}

type NodesJSONOwner struct {
	Contact string `json:"contact"`
}

type NodesJSONSystem struct {
	SiteCode   string `json:"site_code"`
	DomainCode string `json:"domain_code"`
}

type NodesJSONLocation struct {
	Longitude float64 `json:"longitude"`
	Latitude  float64 `json:"latitude"`
}

type NodesJSONSoftware struct {
	Autoupdater *NodesJSONAutoUpdater `json:"autoupdater"`
	Firmware    *NodesJSONFirmware    `json:"firmware"`
}

type NodesJSONAutoUpdater struct {
	Branch  string `json:"branch"`
	Enabled bool   `json:"enabled"`
}

type NodesJSONFirmware struct {
	Base    string `json:"base"`
	Release string `json:"release"`
}

type NodesJSONHardware struct {
	Nproc int    `json:"nproc"`
	Model string `json:"model"`
}

// ParseNodesJSONToMeshviewer converts Yanic nodes.json to MeshviewerData.
func ParseNodesJSONToMeshviewer(data []byte) (*store.MeshviewerData, error) {
	var nj NodesJSONData
	if err := json.Unmarshal(data, &nj); err != nil {
		return nil, err
	}

	mv := &store.MeshviewerData{
		Timestamp: nj.Timestamp,
		Nodes:     make([]store.RawNode, 0, len(nj.Nodes)),
	}

	for _, n := range nj.Nodes {
		nodeID := n.Nodeinfo.NodeID
		if nodeID == "" {
			nodeID = n.Statistics.NodeID
		}
		if nodeID == "" {
			continue
		}

		mac := n.Nodeinfo.Network.MAC
		if mac == "" {
			mac = nodeID
		}

		rn := store.RawNode{
			NodeID:    nodeID,
			Hostname:  n.Nodeinfo.Hostname,
			IsOnline:  store.FlexBool(n.Flags.Online),
			IsGateway: store.FlexBool(n.Flags.Gateway),
			Clients:   store.FlexInt(ifaceToInt(n.Statistics.Clients)),
			Firstseen: n.Firstseen,
			Lastseen:  n.Lastseen,
			MAC:       mac,
			Addresses: n.Nodeinfo.Network.Addresses,
			Gateway:   n.Statistics.Gateway,
			Gateway6:  n.Statistics.Gateway6,
			Domain:    n.Nodeinfo.System.SiteCode,
		}

		if n.Statistics.LoadAvg != nil {
			rn.LoadAvg = store.FlexFloat64(ifaceToFloat(n.Statistics.LoadAvg))
		}
		if n.Statistics.MemoryUsage != nil {
			rn.MemoryUsage = store.FlexFloat64(ifaceToFloat(n.Statistics.MemoryUsage))
		}
		if n.Statistics.RootfsUsage != nil {
			rn.RootfsUsage = store.FlexFloat64(ifaceToFloat(n.Statistics.RootfsUsage))
		}
		if n.Statistics.Uptime != nil {
			rn.Uptime = fmt.Sprintf("%v", n.Statistics.Uptime)
		}
		if n.Nodeinfo.Hardware.Model != "" {
			rn.Model = n.Nodeinfo.Hardware.Model
		}
		if n.Nodeinfo.Hardware.Nproc > 0 {
			rn.Nproc = store.FlexInt(n.Nodeinfo.Hardware.Nproc)
		}
		if n.Nodeinfo.Software.Firmware != nil {
			rn.Firmware = store.RawFirmware{
				Release: n.Nodeinfo.Software.Firmware.Release,
				Base:    n.Nodeinfo.Software.Firmware.Base,
			}
		}
		if n.Nodeinfo.Software.Autoupdater != nil {
			rn.Autoupdater = store.RawAutoUpd{
				Enabled: store.FlexBool(n.Nodeinfo.Software.Autoupdater.Enabled),
				Branch:  n.Nodeinfo.Software.Autoupdater.Branch,
			}
		}
		if n.Nodeinfo.Owner != nil {
			rn.Owner = n.Nodeinfo.Owner.Contact
		}
		if n.Nodeinfo.Location != nil && (n.Nodeinfo.Location.Latitude != 0 || n.Nodeinfo.Location.Longitude != 0) {
			rn.Location = &store.RawLocation{
				Latitude:  n.Nodeinfo.Location.Latitude,
				Longitude: n.Nodeinfo.Location.Longitude,
			}
		}

		mv.Nodes = append(mv.Nodes, rn)
	}

	return mv, nil
}

// --- Helpers ---

// ProbeURL checks if a URL returns a non-HTML 200 response.
// Returns (true, "") on success.
// Returns (false, hostname) if the host is unreachable (timeout/connection error)
// so the caller can skip other URLs on that host.
// Returns (false, "") for non-fatal failures (404, HTML, etc.).
func ProbeURL(client *http.Client, u string) (bool, string) {
	if !urlcheck.IsSafeURL(u) {
		return false, ""
	}
	parsed, _ := url.Parse(u)
	host := ""
	if parsed != nil {
		host = parsed.Hostname()
	}

	req, err := http.NewRequest("HEAD", u, nil)
	if err != nil {
		return false, ""
	}
	req.Header.Set("User-Agent", "freifunk-map-modern/1.0")

	resp, err := client.Do(req)
	if err != nil {
		errStr := err.Error()
		// Timeout, connection, DNS, or TLS errors → mark host as dead
		if strings.Contains(errStr, "deadline exceeded") ||
			strings.Contains(errStr, "connection refused") ||
			strings.Contains(errStr, "no such host") ||
			strings.Contains(errStr, "no route to host") ||
			strings.Contains(errStr, "network is unreachable") ||
			strings.Contains(errStr, "tls:") {
			return false, host
		}
		return false, ""
	}
	resp.Body.Close()

	if resp.StatusCode != 200 {
		return false, ""
	}

	// Reject HTML responses — SPA meshviewers (e.g. Bremen) return 200
	// with text/html for any path, including /data/meshviewer.json.
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/html") {
		return false, ""
	}

	return true, ""
}

func CollectMapBases(c Community) []string {
	seen := make(map[string]bool)
	var bases []string
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			bases = append(bases, u)
		}
	}
	for _, u := range c.MeshviewerURLs {
		for _, b := range DeriveMeshviewerBases(u) {
			add(b)
		}
	}
	for _, u := range c.NodelistURLs {
		for _, b := range DeriveMeshviewerBases(u) {
			add(b)
		}
	}
	return bases
}

func DeriveMeshviewerBases(dataURL string) []string {
	var bases []string
	if idx := strings.LastIndex(dataURL, "/data/"); idx > 0 {
		bases = append(bases, dataURL[:idx])
	}
	if idx := strings.LastIndex(dataURL, "/"); idx > 8 {
		base := dataURL[:idx]
		if len(bases) == 0 || bases[0] != base {
			bases = append(bases, base)
		}
	}
	return bases
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// --- Type-coercion helpers ---

func ifaceToBool(v interface{}) bool {
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return val == "true" || val == "1" || val == "yes"
	case float64:
		return val != 0
	case int:
		return val != 0
	default:
		return false
	}
}

func ifaceToString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == math.Trunc(val) {
			return fmt.Sprintf("%.0f", val)
		}
		return fmt.Sprintf("%g", val)
	case json.Number:
		return val.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", val)
	}
}

func ifaceToInt(v interface{}) int {
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case string:
		n := 0
		fmt.Sscanf(val, "%d", &n)
		return n
	case json.Number:
		n, _ := val.Int64()
		return int(n)
	default:
		return 0
	}
}

func ifaceToFloat(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case string:
		f := 0.0
		fmt.Sscanf(val, "%f", &f)
		return f
	case json.Number:
		f, _ := val.Float64()
		return f
	default:
		return 0
	}
}
