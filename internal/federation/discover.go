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

			if tt == "meshviewer" && strings.HasSuffix(u, ".json") {
				c.MeshviewerURLs = append(c.MeshviewerURLs, u)
			} else if tt == "nodelist" && strings.HasSuffix(u, ".json") {
				c.NodelistURLs = append(c.NodelistURLs, u)
			} else if tt == "nodelist" {
				c.NodelistURLs = append(c.NodelistURLs, u)
			} else if tt == "meshviewer" || tt == "ffmap" || tt == "hopglass" {
				base := strings.TrimSuffix(u, "/")
				c.MeshviewerURLs = append(c.MeshviewerURLs, base+"/data/meshviewer.json")
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
	var mergedComms []Community
	for i := range communities {
		c := &communities[i]
		existing, ok := nameMap[c.Name]
		if !ok {
			merged := *c
			merged.AllKeys = []string{c.Key}
			nameMap[c.Name] = &merged
			mergedComms = append(mergedComms, merged)
			continue
		}
		existing.AllKeys = append(existing.AllKeys, c.Key)
		if c.Nodes > existing.Nodes {
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
		for j := range mergedComms {
			if mergedComms[j].Key == existing.Key {
				mergedComms[j] = *existing
				break
			}
		}
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

			// Probe ALL meshviewer URLs — communities may have multiple
			// distinct data sources (e.g. different domains/subpaths)
			for _, u := range c.MeshviewerURLs {
				if ProbeURL(client, u) {
					ch <- result{source: CommunitySource{
						CommunityKey: c.Key, CommunityKeys: c.AllKeys,
						DataURL: u, DataType: "meshviewer",
						GrafanaURL: c.GrafanaURL, MapURLs: mapURLs,
					}, ok: true}
					found = true
				}
			}

			// Only try nodelists if no meshviewer source worked
			if !found {
				for _, u := range c.NodelistURLs {
					if ProbeURL(client, u) {
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
			Clients:  ifaceToInt(n.Status.Clients),
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

// --- Helpers ---

func ProbeURL(client *http.Client, u string) bool {
	if !urlcheck.IsSafeURL(u) {
		return false
	}
	req, err := http.NewRequest("HEAD", u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "freifunk-map-modern/1.0")

	c := &http.Client{Timeout: 8 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()

	return resp.StatusCode == 200
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
