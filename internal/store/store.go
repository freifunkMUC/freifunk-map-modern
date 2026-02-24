package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/freifunkMUC/freifunk-map-modern/internal/config"
)

// FlexBool handles JSON booleans that may be encoded as bool, string ("1"/"0"/""), or number.
type FlexBool bool

func (fb *FlexBool) UnmarshalJSON(data []byte) error {
	var raw interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch v := raw.(type) {
	case bool:
		*fb = FlexBool(v)
	case string:
		*fb = FlexBool(v == "true" || v == "1" || v == "yes")
	case float64:
		*fb = FlexBool(v != 0)
	default:
		*fb = false
	}
	return nil
}

// --- Raw JSON from meshviewer.json ---

type MeshviewerData struct {
	Timestamp string    `json:"timestamp"`
	Nodes     []RawNode `json:"nodes"`
	Links     []RawLink `json:"links,omitempty"`
}

type RawNode struct {
	Firstseen   string       `json:"firstseen"`
	Lastseen    string       `json:"lastseen"`
	IsOnline    FlexBool     `json:"is_online"`
	IsGateway   FlexBool     `json:"is_gateway"`
	Clients     int          `json:"clients"`
	ClientsW24  int          `json:"clients_wifi24"`
	ClientsW5   int          `json:"clients_wifi5"`
	ClientsOth  int          `json:"clients_other"`
	RootfsUsage float64      `json:"rootfs_usage"`
	LoadAvg     float64      `json:"loadavg"`
	MemoryUsage float64      `json:"memory_usage"`
	Uptime      string       `json:"uptime"`
	GwNexthop   string       `json:"gateway_nexthop"`
	Gateway     string       `json:"gateway"`
	Gateway6    string       `json:"gateway6"`
	NodeID      string       `json:"node_id"`
	MAC         string       `json:"mac"`
	Addresses   []string     `json:"addresses"`
	Domain      string       `json:"domain"`
	Hostname    string       `json:"hostname"`
	Owner       string       `json:"owner"`
	Location    *RawLocation `json:"location,omitempty"`
	Firmware    RawFirmware  `json:"firmware"`
	Autoupdater RawAutoUpd   `json:"autoupdater"`
	Nproc       int          `json:"nproc"`
	Model       string       `json:"model"`
}

type RawLocation struct {
	Longitude float64 `json:"longitude"`
	Latitude  float64 `json:"latitude"`
}

type RawFirmware struct {
	Base      string `json:"base"`
	Release   string `json:"release"`
	Target    string `json:"target,omitempty"`
	Subtarget string `json:"subtarget,omitempty"`
	ImageName string `json:"image_name,omitempty"`
}

type RawAutoUpd struct {
	Enabled FlexBool `json:"enabled"`
	Branch  string   `json:"branch"`
}

type RawLink struct {
	Source   string  `json:"source"`
	Target   string  `json:"target"`
	SourceTQ float64 `json:"source_tq"`
	TargetTQ float64 `json:"target_tq"`
	Type     string  `json:"type"`
}

// --- Processed API types ---

type Node struct {
	NodeID      string   `json:"node_id"`
	Hostname    string   `json:"hostname"`
	IsOnline    bool     `json:"is_online"`
	IsGateway   bool     `json:"is_gateway"`
	Clients     int      `json:"clients"`
	ClientsW24  int      `json:"clients_wifi24"`
	ClientsW5   int      `json:"clients_wifi5"`
	ClientsOth  int      `json:"clients_other"`
	Domain      string   `json:"domain"`
	DomainName  string   `json:"domain_name,omitempty"`
	Community   string   `json:"community,omitempty"`
	Communities []string `json:"communities,omitempty"`
	Model       string   `json:"model,omitempty"`
	Firmware    string   `json:"firmware,omitempty"`
	FWBase      string   `json:"fw_base,omitempty"`
	Autoupdater bool     `json:"autoupdater"`
	Branch      string   `json:"branch,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	MAC         string   `json:"mac"`
	Lat         *float64 `json:"lat,omitempty"`
	Lng         *float64 `json:"lng,omitempty"`
	Uptime      string   `json:"uptime,omitempty"`
	LoadAvg     float64  `json:"load_avg"`
	MemUsage    float64  `json:"mem_usage"`
	RootfsUsage float64  `json:"rootfs_usage"`
	Gateway     string   `json:"gateway,omitempty"`
	Firstseen   string   `json:"firstseen"`
	Lastseen    string   `json:"lastseen"`
	Nproc       int      `json:"nproc"`
	Addresses   []string `json:"addresses,omitempty"`
	ImageName   string   `json:"image_name,omitempty"`
	Neighbours  []string `json:"neighbours,omitempty"`
}

type Link struct {
	Source   string  `json:"source"`
	Target   string  `json:"target"`
	SourceTQ float64 `json:"source_tq"`
	TargetTQ float64 `json:"target_tq"`
	Type     string  `json:"type"`
	Distance float64 `json:"distance,omitempty"`
}

type Stats struct {
	TotalNodes    int            `json:"total_nodes"`
	OnlineNodes   int            `json:"online_nodes"`
	TotalClients  int            `json:"total_clients"`
	Gateways      int            `json:"gateways"`
	Domains       map[string]int `json:"domains"`
	Models        map[string]int `json:"models"`
	Firmwares     map[string]int `json:"firmwares"`
	GluonVersions map[string]int `json:"gluon_versions"`
	Communities   map[string]int `json:"communities"`
	Timestamp     string         `json:"timestamp"`
}

type Snapshot struct {
	Nodes     map[string]*Node `json:"-"`
	NodeList  []*Node          `json:"nodes"`
	Links     []Link           `json:"links"`
	Stats     Stats            `json:"stats"`
	Timestamp time.Time        `json:"timestamp"`
}

// --- SSE diff types ---

type NodeDiff struct {
	NodeID   string  `json:"node_id"`
	Hostname string  `json:"hostname"`
	IsOnline bool    `json:"is_online"`
	Clients  int     `json:"clients"`
	LoadAvg  float64 `json:"load_avg"`
	MemUsage float64 `json:"mem_usage"`
}

type SSEUpdate struct {
	Type    string     `json:"type"`
	Stats   Stats      `json:"stats"`
	Changed []NodeDiff `json:"changed,omitempty"`
	Gone    []string   `json:"gone,omitempty"`
	New     []string   `json:"new,omitempty"`
}

// SSEBroadcaster is the interface the store needs from the SSE hub.
type SSEBroadcaster interface {
	Broadcast(update interface{})
	ClientCount() int
}

// --- Store ---

type Store struct {
	Cfg      *config.Config
	mu       sync.RWMutex
	snapshot *Snapshot
	client   *http.Client
}

func New(cfg *config.Config) *Store {
	return &Store{
		Cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
		snapshot: &Snapshot{
			Nodes: make(map[string]*Node),
			Stats: Stats{
				Domains:       map[string]int{},
				Models:        map[string]int{},
				Firmwares:     map[string]int{},
				GluonVersions: map[string]int{},
				Communities:   map[string]int{},
			},
		},
	}
}

func (s *Store) GetSnapshot() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *Store) SetSnapshot(snap *Snapshot) {
	s.mu.Lock()
	s.snapshot = snap
	s.mu.Unlock()
}

func (s *Store) Refresh() error {
	resp, err := s.client.Get(s.Cfg.DataURL)
	if err != nil {
		return fmt.Errorf("fetching data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("unexpected status %d from data source", resp.StatusCode)
	}

	const maxBodySize = 20 * 1024 * 1024 // 20 MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return fmt.Errorf("reading body: %w", err)
	}

	var raw MeshviewerData
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("parsing JSON: %w", err)
	}

	snap := s.ProcessData(&raw)

	s.mu.Lock()
	s.snapshot = snap
	s.mu.Unlock()

	return nil
}

func (s *Store) RunRefreshLoop(ctx context.Context, hub SSEBroadcaster) {
	ticker := time.NewTicker(s.Cfg.RefreshDuration)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			old := s.GetSnapshot()
			if err := s.Refresh(); err != nil {
				log.Printf("Data refresh error: %v", err)
				continue
			}
			snap := s.GetSnapshot()
			log.Printf("Data refreshed: %d nodes (%d online), %d clients, %d links, %d SSE clients",
				snap.Stats.TotalNodes, snap.Stats.OnlineNodes, snap.Stats.TotalClients,
				len(snap.Links), hub.ClientCount())

			diff := ComputeDiff(old, snap)
			if diff != nil {
				hub.Broadcast(diff)
			}
		}
	}
}

func (s *Store) ProcessData(raw *MeshviewerData) *Snapshot {
	nodes := make(map[string]*Node, len(raw.Nodes))
	nodeList := make([]*Node, 0, len(raw.Nodes))

	stats := Stats{
		Domains:       make(map[string]int),
		Models:        make(map[string]int),
		Firmwares:     make(map[string]int),
		GluonVersions: make(map[string]int),
		Communities:   make(map[string]int),
		Timestamp:     raw.Timestamp,
	}

	for i := range raw.Nodes {
		rn := &raw.Nodes[i]
		n := &Node{
			NodeID:      rn.NodeID,
			Hostname:    rn.Hostname,
			IsOnline:    bool(rn.IsOnline),
			IsGateway:   bool(rn.IsGateway),
			Clients:     rn.Clients,
			ClientsW24:  rn.ClientsW24,
			ClientsW5:   rn.ClientsW5,
			ClientsOth:  rn.ClientsOth,
			Domain:      rn.Domain,
			Model:       rn.Model,
			Firmware:    rn.Firmware.Release,
			FWBase:      rn.Firmware.Base,
			Autoupdater: bool(rn.Autoupdater.Enabled),
			Branch:      rn.Autoupdater.Branch,
			Owner:       rn.Owner,
			MAC:         rn.MAC,
			Uptime:      rn.Uptime,
			LoadAvg:     rn.LoadAvg,
			MemUsage:    rn.MemoryUsage,
			RootfsUsage: rn.RootfsUsage,
			Gateway:     rn.Gateway,
			Firstseen:   rn.Firstseen,
			Lastseen:    rn.Lastseen,
			Nproc:       rn.Nproc,
			Addresses:   rn.Addresses,
			ImageName:   rn.Firmware.ImageName,
		}

		if dn, ok := s.Cfg.DomainNames[rn.Domain]; ok {
			n.DomainName = dn
		}

		if rn.Location != nil &&
			math.Abs(rn.Location.Latitude) < 90 &&
			math.Abs(rn.Location.Longitude) < 180 &&
			(rn.Location.Latitude != 0 || rn.Location.Longitude != 0) {
			lat := rn.Location.Latitude
			lng := rn.Location.Longitude
			n.Lat = &lat
			n.Lng = &lng
		}

		nodes[rn.NodeID] = n
		nodeList = append(nodeList, n)

		stats.TotalNodes++
		if bool(rn.IsOnline) {
			stats.OnlineNodes++
			stats.TotalClients += rn.Clients
		}
		if bool(rn.IsGateway) {
			stats.Gateways++
		}
		if rn.Domain != "" {
			dn := rn.Domain
			if name, ok := s.Cfg.DomainNames[dn]; ok {
				dn = name
			}
			stats.Domains[dn]++
		}
		if rn.Model != "" {
			stats.Models[rn.Model]++
		}
		if rn.Firmware.Release != "" {
			stats.Firmwares[rn.Firmware.Release]++
		}
		if rn.Firmware.Base != "" {
			stats.GluonVersions[rn.Firmware.Base]++
		}
	}

	// Process links & build neighbour lists
	links := make([]Link, 0, len(raw.Links))
	for _, rl := range raw.Links {
		l := Link{
			Source:   rl.Source,
			Target:   rl.Target,
			SourceTQ: rl.SourceTQ,
			TargetTQ: rl.TargetTQ,
			Type:     rl.Type,
		}

		sn, sok := nodes[rl.Source]
		tn, tok := nodes[rl.Target]
		if sok && tok && sn.Lat != nil && tn.Lat != nil {
			l.Distance = Haversine(*sn.Lat, *sn.Lng, *tn.Lat, *tn.Lng)
		}

		if sok {
			sn.Neighbours = AppendUnique(sn.Neighbours, rl.Target)
		}
		if tok {
			tn.Neighbours = AppendUnique(tn.Neighbours, rl.Source)
		}

		links = append(links, l)
	}

	sort.Slice(nodeList, func(i, j int) bool {
		if nodeList[i].IsOnline != nodeList[j].IsOnline {
			return nodeList[i].IsOnline
		}
		return strings.ToLower(nodeList[i].Hostname) < strings.ToLower(nodeList[j].Hostname)
	})

	ts, _ := time.Parse(time.RFC3339, raw.Timestamp)

	return &Snapshot{
		Nodes:     nodes,
		NodeList:  nodeList,
		Links:     links,
		Stats:     stats,
		Timestamp: ts,
	}
}

// ComputeDiff computes an SSE update between two snapshots.
func ComputeDiff(old, cur *Snapshot) *SSEUpdate {
	if old == nil || len(old.Nodes) == 0 {
		return &SSEUpdate{Type: "full", Stats: cur.Stats}
	}

	upd := &SSEUpdate{Type: "diff", Stats: cur.Stats}

	for id, nn := range cur.Nodes {
		on, exists := old.Nodes[id]
		if !exists {
			upd.New = append(upd.New, id)
			continue
		}
		if on.IsOnline != nn.IsOnline || on.Clients != nn.Clients ||
			on.LoadAvg != nn.LoadAvg || on.MemUsage != nn.MemUsage {
			upd.Changed = append(upd.Changed, NodeDiff{
				NodeID:   id,
				Hostname: nn.Hostname,
				IsOnline: nn.IsOnline,
				Clients:  nn.Clients,
				LoadAvg:  nn.LoadAvg,
				MemUsage: nn.MemUsage,
			})
		}
	}

	for id := range old.Nodes {
		if _, ok := cur.Nodes[id]; !ok {
			upd.Gone = append(upd.Gone, id)
		}
	}

	if len(upd.Changed) == 0 && len(upd.New) == 0 && len(upd.Gone) == 0 {
		return &SSEUpdate{Type: "stats", Stats: cur.Stats}
	}

	return upd
}

// --- Helpers ---

func Haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371000
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}

func AppendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}
