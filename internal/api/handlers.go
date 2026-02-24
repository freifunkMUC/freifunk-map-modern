package api

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/freifunkMUC/freifunk-map-modern/internal/config"
	"github.com/freifunkMUC/freifunk-map-modern/internal/federation"
	"github.com/freifunkMUC/freifunk-map-modern/internal/sse"
	"github.com/freifunkMUC/freifunk-map-modern/internal/store"
	"github.com/freifunkMUC/freifunk-map-modern/internal/urlcheck"
)

// GzipHandler wraps an http.Handler with gzip compression.
func GzipHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/events") {
			next.ServeHTTP(w, r)
			return
		}
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")

		gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		defer gz.Close()

		gzw := &gzipResponseWriter{Writer: gz, ResponseWriter: w}
		next.ServeHTTP(gzw, r)
	})
}

type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

// RegisterHandlers registers core API routes.
func RegisterHandlers(mux *http.ServeMux, cfg *config.Config, s *store.Store, hub *sse.Hub) {
	mux.HandleFunc("/api/nodes", handleNodes(s))
	mux.HandleFunc("/api/nodes/", handleNodeDetail(s))
	mux.HandleFunc("/api/links", handleLinks(s))
	mux.HandleFunc("/api/stats", handleStats(s))
	mux.HandleFunc("/api/config", handleClientConfig(cfg))
	mux.HandleFunc("/api/events", sse.HandleSSE(hub))
}

// RegisterFederationHandlers registers federation-specific routes.
func RegisterFederationHandlers(mux *http.ServeMux, cfg *config.Config, fs *federation.Store) {
	mux.HandleFunc("/api/communities", handleCommunities(fs))
	mux.HandleFunc("/api/metrics/", handleNodeMetrics(cfg, fs))
}

// RegisterMetricsHandler registers the metrics route for single-community mode.
func RegisterMetricsHandler(mux *http.ServeMux, cfg *config.Config) {
	mux.HandleFunc("/api/metrics/", handleNodeMetrics(cfg, nil))
}

func jsonResponse(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=30")
	json.NewEncoder(w).Encode(v)
}

func handleNodes(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := s.GetSnapshot()
		jsonResponse(w, snap.NodeList)
	}
}

func handleNodeDetail(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/nodes/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			http.Error(w, "node_id required", http.StatusBadRequest)
			return
		}
		nodeID := parts[0]

		snap := s.GetSnapshot()
		node, ok := snap.Nodes[nodeID]
		if !ok {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}

		type NeighbourInfo struct {
			NodeID   string  `json:"node_id"`
			Hostname string  `json:"hostname"`
			IsOnline bool    `json:"is_online"`
			LinkType string  `json:"link_type,omitempty"`
			TQ       float64 `json:"tq,omitempty"`
			Distance float64 `json:"distance,omitempty"`
		}

		type NodeDetail struct {
			*store.Node
			NeighbourDetails []NeighbourInfo `json:"neighbour_details"`
		}

		detail := NodeDetail{Node: node}
		for _, nid := range node.Neighbours {
			ni := NeighbourInfo{NodeID: nid}
			if nn, ok := snap.Nodes[nid]; ok {
				ni.Hostname = nn.Hostname
				ni.IsOnline = nn.IsOnline
			}
			for _, l := range snap.Links {
				if (l.Source == nodeID && l.Target == nid) || (l.Target == nodeID && l.Source == nid) {
					ni.LinkType = l.Type
					ni.TQ = (l.SourceTQ + l.TargetTQ) / 2
					ni.Distance = l.Distance
					break
				}
			}
			detail.NeighbourDetails = append(detail.NeighbourDetails, ni)
		}

		jsonResponse(w, detail)
	}
}

func handleLinks(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := s.GetSnapshot()
		jsonResponse(w, snap.Links)
	}
}

func handleStats(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := s.GetSnapshot()
		jsonResponse(w, snap.Stats)
	}
}

func handleCommunities(fs *federation.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		communities := fs.GetCommunities()
		sources := fs.GetSources()
		grafanaCache := fs.GetGrafanaCache()

		sourceMap := make(map[string]string)
		for _, s := range sources {
			sourceMap[s.CommunityKey] = s.DataType
		}

		type CommunityInfo struct {
			Key          string  `json:"key"`
			Name         string  `json:"name"`
			URL          string  `json:"url"`
			Lat          float64 `json:"lat,omitempty"`
			Lng          float64 `json:"lng,omitempty"`
			Nodes        int     `json:"nodes,omitempty"`
			DataType     string  `json:"data_type,omitempty"`
			GrafanaURL   string  `json:"grafana_url,omitempty"`
			DashboardURL string  `json:"dashboard_url,omitempty"`
			Meta         string  `json:"metacommunity,omitempty"`
			Active       bool    `json:"active"`
		}

		result := make([]CommunityInfo, 0, len(communities))
		for _, c := range communities {
			dt, active := sourceMap[c.Key]
			ci := CommunityInfo{
				Key:        c.Key,
				Name:       c.Name,
				URL:        c.URL,
				Lat:        c.Lat,
				Lng:        c.Lng,
				Nodes:      c.Nodes,
				DataType:   dt,
				GrafanaURL: c.GrafanaURL,
				Meta:       c.Metacommunity,
				Active:     active,
			}
			if info, ok := grafanaCache[c.Key]; ok {
				if ci.GrafanaURL == "" {
					ci.GrafanaURL = info.BaseURL
				}
				ci.DashboardURL = info.DashboardURL
			}
			result = append(result, ci)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		json.NewEncoder(w).Encode(result)
	}
}

func handleClientConfig(cfg *config.Config) http.HandlerFunc {
	type ClientConfig struct {
		SiteName         string                `json:"siteName"`
		MapCenter        [2]float64            `json:"mapCenter"`
		MapZoom          int                   `json:"mapZoom"`
		TileLayers       []config.TileLayer    `json:"tileLayers"`
		DomainNames      map[string]string     `json:"domainNames"`
		Links            []config.ExternalLink `json:"links"`
		DevicePictureURL string                `json:"devicePictureURL"`
		EolInfoURL       string                `json:"eolInfoURL,omitempty"`
		GrafanaURL       string                `json:"grafanaURL"`
		GrafanaDashboard string                `json:"grafanaDashboard"`
		HasGrafana       bool                  `json:"hasGrafana"`
		Federation       bool                  `json:"federation"`
	}

	cc := ClientConfig{
		SiteName:         cfg.SiteName,
		MapCenter:        cfg.MapCenter,
		MapZoom:          cfg.MapZoom,
		TileLayers:       cfg.TileLayers,
		DomainNames:      cfg.DomainNames,
		Links:            cfg.Links,
		DevicePictureURL: cfg.DevicePictureURL,
		EolInfoURL:       cfg.EolInfoURL,
		GrafanaURL:       cfg.GrafanaURL,
		GrafanaDashboard: cfg.GrafanaDashboard,
		HasGrafana:       cfg.GrafanaURL != "",
		Federation:       cfg.Federation,
	}

	data, _ := json.Marshal(cc)

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(data)
	}
}

func handleNodeMetrics(cfg *config.Config, fedStore *federation.Store) http.HandlerFunc {
	client := &http.Client{Timeout: 15 * time.Second}

	queries := map[string]string{
		"clients":         `SELECT round(mean("clients.total")) FROM "node" WHERE ("nodeid" =~ /^%s$/) AND time >= now() - %s GROUP BY time(%s) fill(null)`,
		"traffic_forward": `SELECT non_negative_derivative(mean("traffic.forward.bytes"), 1s) * 8 FROM "node" WHERE ("nodeid" =~ /^%s$/) AND time >= now() - %s GROUP BY time(%s) fill(none)`,
		"traffic_rx":      `SELECT non_negative_derivative(mean("traffic.rx.bytes"), 1s) * 8 FROM "node" WHERE ("nodeid" =~ /^%s$/) AND time >= now() - %s GROUP BY time(%s) fill(none)`,
		"traffic_tx":      `SELECT non_negative_derivative(mean("traffic.tx.bytes"), 1s) * 8 FROM "node" WHERE ("nodeid" =~ /^%s$/) AND time >= now() - %s GROUP BY time(%s) fill(none)`,
		"load":            `SELECT mean("load") FROM "node" WHERE ("nodeid" =~ /^%s$/) AND time >= now() - %s GROUP BY time(%s) fill(null)`,
		"memory":          `SELECT mean("memory.usage") FROM "node" WHERE ("nodeid" =~ /^%s$/) AND time >= now() - %s GROUP BY time(%s) fill(null)`,
	}

	return func(w http.ResponseWriter, r *http.Request) {
		nodeID := strings.TrimPrefix(r.URL.Path, "/api/metrics/")
		nodeID = strings.Split(nodeID, "/")[0]
		if nodeID == "" {
			http.Error(w, "node_id required", http.StatusBadRequest)
			return
		}

		var grafanaURL string
		var dsID int
		var dbName string
		var queryNodeID string

		if fedStore != nil {
			info, originalID := fedStore.GrafanaInfoForNode(nodeID)
			if info.BaseURL == "" || info.DatasourceID == 0 {
				http.Error(w, "no Grafana datasource for this community", http.StatusNotFound)
				return
			}
			grafanaURL = info.BaseURL
			dsID = info.DatasourceID
			dbName = info.Database
			if dbName == "" {
				dbName = "yanic"
			}
			queryNodeID = originalID
		} else {
			if cfg.GrafanaURL == "" {
				http.Error(w, "Grafana not configured", http.StatusServiceUnavailable)
				return
			}
			grafanaURL = cfg.GrafanaURL
			dsID = 5
			dbName = "yanic"
			queryNodeID = nodeID
		}

		// Sanitize queryNodeID: allow hex, colons, dashes only
		for _, c := range queryNodeID {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') || c == ':' || c == '-') {
				http.Error(w, "invalid node_id", http.StatusBadRequest)
				return
			}
		}

		metric := r.URL.Query().Get("metric")
		if metric == "" {
			metric = "clients"
		}

		duration := r.URL.Query().Get("duration")
		if duration == "" {
			duration = "24h"
		}
		validDurations := map[string]string{
			"6h": "1m", "12h": "2m", "24h": "5m", "48h": "10m",
			"7d": "30m", "14d": "1h", "30d": "2h",
		}
		interval, ok := validDurations[duration]
		if !ok {
			duration = "24h"
			interval = "5m"
		}

		type MetricResult struct {
			Name   string    `json:"name"`
			Times  []int64   `json:"times"`
			Values []float64 `json:"values"`
		}

		var metricNames []string
		if metric == "traffic" {
			metricNames = []string{"traffic_forward", "traffic_rx", "traffic_tx"}
		} else {
			metricNames = []string{metric}
		}

		results := make([]MetricResult, 0, len(metricNames))

		for _, mn := range metricNames {
			queryTpl, found := queries[mn]
			if !found {
				continue
			}

			influxQuery := fmt.Sprintf(queryTpl, queryNodeID, duration, interval)

			dsURL := fmt.Sprintf("%s/api/datasources/proxy/%d/query?db=%s&q=%s&epoch=s",
				grafanaURL, dsID, url.QueryEscape(dbName), url.QueryEscape(influxQuery))

			if !urlcheck.IsSafeURL(dsURL) {
				continue
			}

			req, err := http.NewRequestWithContext(r.Context(), "GET", dsURL, nil)
			if err != nil {
				continue
			}
			req.Header.Set("Accept", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
			resp.Body.Close()
			if err != nil || resp.StatusCode != 200 {
				continue
			}

			var influxResp struct {
				Results []struct {
					Series []struct {
						Name    string          `json:"name"`
						Columns []string        `json:"columns"`
						Values  [][]interface{} `json:"values"`
					} `json:"series"`
				} `json:"results"`
			}
			if err := json.Unmarshal(body, &influxResp); err != nil {
				continue
			}

			mr := MetricResult{Name: mn}
			if len(influxResp.Results) > 0 && len(influxResp.Results[0].Series) > 0 {
				series := influxResp.Results[0].Series[0]
				for _, row := range series.Values {
					if len(row) < 2 {
						continue
					}
					var ts int64
					switch t := row[0].(type) {
					case float64:
						ts = int64(t)
					case json.Number:
						ts64, _ := t.Int64()
						ts = ts64
					}
					var val float64
					if row[1] != nil {
						switch v := row[1].(type) {
						case float64:
							val = v
						case json.Number:
							val64, _ := v.Float64()
							val = val64
						}
					}
					mr.Times = append(mr.Times, ts)
					mr.Values = append(mr.Values, val)
				}
			}
			results = append(results, mr)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=60")
		json.NewEncoder(w).Encode(results)
	}
}
