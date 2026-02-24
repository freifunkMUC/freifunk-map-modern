package federation

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const grafanaCacheFile = "grafana_cache.json"

// GrafanaInfo stores discovered Grafana info for a community.
type GrafanaInfo struct {
	BaseURL      string               `json:"base_url"`
	DashboardURL string               `json:"dashboard_url,omitempty"`
	DatasourceID int                  `json:"datasource_id,omitempty"`
	Database     string               `json:"database,omitempty"`
	DataPaths    []string             `json:"data_paths,omitempty"`
	RenderImages []GrafanaRenderImage `json:"render_images,omitempty"`
}

// GrafanaRenderImage is a Grafana render/image URL template.
type GrafanaRenderImage struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

// GrafanaCache maps community key -> GrafanaInfo.
type GrafanaCache map[string]GrafanaInfo

var grafanaURLPattern = regexp.MustCompile(`https?://[^"'\s,}]+(?:grafana|stats)[^"'\s,}]*`)

func LoadGrafanaCache() GrafanaCache {
	data, err := os.ReadFile(grafanaCacheFile)
	if err != nil {
		return make(GrafanaCache)
	}
	var cache GrafanaCache
	if err := json.Unmarshal(data, &cache); err != nil {
		var old map[string]string
		if err2 := json.Unmarshal(data, &old); err2 == nil {
			cache = make(GrafanaCache)
			for k, v := range old {
				cache[k] = GrafanaInfo{BaseURL: v}
			}
			log.Printf("Grafana cache: migrated %d old-format entries", len(cache))
			SaveGrafanaCache(cache)
			return cache
		}
		return make(GrafanaCache)
	}
	log.Printf("Grafana cache: loaded %d entries from %s", len(cache), grafanaCacheFile)
	return cache
}

func SaveGrafanaCache(cache GrafanaCache) {
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(grafanaCacheFile, data, 0644)
	log.Printf("Grafana cache: saved %d entries to %s", len(cache), grafanaCacheFile)
}

// DiscoverGrafanaURLs probes meshviewer config.json for each community to find
// Grafana base URLs and per-node dashboard templates.
func DiscoverGrafanaURLs(client *http.Client, sources []CommunitySource, communities []Community) GrafanaCache {
	cache := LoadGrafanaCache()

	for _, c := range communities {
		if c.GrafanaURL != "" {
			if _, exists := cache[c.Key]; !exists {
				cache[c.Key] = GrafanaInfo{BaseURL: c.GrafanaURL}
			}
		}
	}

	var needDiscovery []CommunitySource
	existingKeys := make(map[string]bool)
	for _, src := range sources {
		entry, exists := cache[src.CommunityKey]
		if !exists || (entry.BaseURL != "" && entry.DashboardURL == "") {
			needDiscovery = append(needDiscovery, src)
		}
		existingKeys[src.CommunityKey] = true
	}
	for _, c := range communities {
		if existingKeys[c.Key] {
			continue
		}
		if _, exists := cache[c.Key]; exists {
			continue
		}
		mapURLs := CollectMapBases(c)
		if len(mapURLs) > 0 {
			needDiscovery = append(needDiscovery, CommunitySource{
				CommunityKey:  c.Key,
				CommunityKeys: c.AllKeys,
				MapURLs:       mapURLs,
			})
		}
	}

	if len(needDiscovery) == 0 {
		log.Printf("Grafana discovery: all %d communities already cached", len(sources))
		return cache
	}

	log.Printf("Grafana discovery: probing config.json for %d communities...", len(needDiscovery))

	type result struct {
		key  string
		info GrafanaInfo
	}

	ch := make(chan result, len(needDiscovery))
	sem := make(chan struct{}, 50)
	var wg sync.WaitGroup

	for _, src := range needDiscovery {
		wg.Add(1)
		go func(src CommunitySource) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			info := discoverGrafanaForSource(client, src)
			if info.BaseURL != "" || len(info.DataPaths) > 0 {
				ch <- result{key: src.CommunityKey, info: info}
			}
		}(src)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	newFound := 0
	for r := range ch {
		cache[r.key] = r.info
		newFound++
	}

	log.Printf("Grafana discovery: found %d new Grafana entries (total cached: %d)", newFound, len(cache))

	// Discover datasource IDs
	probeDS := 0
	for _, info := range cache {
		if info.BaseURL != "" && info.DatasourceID == 0 {
			probeDS++
		}
	}
	if probeDS > 0 {
		log.Printf("Grafana discovery: probing /api/datasources for %d communities...", probeDS)
		dsCh := make(chan result, probeDS)
		dsSem := make(chan struct{}, 40)
		var dsWg sync.WaitGroup
		for key, info := range cache {
			if info.BaseURL != "" && info.DatasourceID == 0 {
				dsWg.Add(1)
				go func(key string, info GrafanaInfo) {
					defer dsWg.Done()
					dsSem <- struct{}{}
					defer func() { <-dsSem }()
					updated := discoverDatasource(client, info)
					if updated.DatasourceID != 0 {
						dsCh <- result{key: key, info: updated}
					}
				}(key, info)
			}
		}
		go func() { dsWg.Wait(); close(dsCh) }()
		dsFound := 0
		for r := range dsCh {
			cache[r.key] = r.info
			dsFound++
		}
		log.Printf("Grafana discovery: found %d datasources", dsFound)
	}

	SaveGrafanaCache(cache)
	return cache
}

func discoverGrafanaForSource(client *http.Client, src CommunitySource) GrafanaInfo {
	seen := make(map[string]bool)
	var baseURLs []string
	for _, b := range DeriveMeshviewerBases(src.DataURL) {
		if !seen[b] {
			seen[b] = true
			baseURLs = append(baseURLs, b)
		}
	}
	for _, b := range src.MapURLs {
		if !seen[b] {
			seen[b] = true
			baseURLs = append(baseURLs, b)
		}
	}

	probeClient := &http.Client{Timeout: 8 * time.Second}

	for _, base := range baseURLs {
		configURL := base + "/config.json"
		req, err := http.NewRequest("GET", configURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "freifunk-map-modern/1.0")

		resp, err := probeClient.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			continue
		}

		if info := extractGrafanaFromConfig(body); info.BaseURL != "" {
			return info
		}

		if matches := grafanaURLPattern.FindAll(body, 3); len(matches) > 0 {
			for _, m := range matches {
				u := strings.TrimRight(string(m), "\"'>,;)")
				if strings.Contains(u, "grafana") || strings.Contains(u, "stats.") {
					return GrafanaInfo{BaseURL: u}
				}
			}
		}
	}

	for _, base := range baseURLs {
		req, err := http.NewRequest("GET", base+"/", nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "freifunk-map-modern/1.0")

		resp, err := probeClient.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			continue
		}

		if info := extractGrafanaFromInlineConfig(body); info.BaseURL != "" || len(info.DataPaths) > 0 {
			return info
		}
	}

	return GrafanaInfo{}
}

func extractGrafanaFromInlineConfig(body []byte) GrafanaInfo {
	var info GrafanaInfo
	text := string(body)

	if idx := strings.Index(text, "dataPath:"); idx >= 0 {
		sub := text[idx:]
		start := strings.Index(sub, "[")
		end := strings.Index(sub, "]")
		if start >= 0 && end > start {
			arrStr := sub[start : end+1]
			var paths []string
			if err := json.Unmarshal([]byte(arrStr), &paths); err == nil {
				for _, p := range paths {
					p = strings.TrimSuffix(p, "/")
					info.DataPaths = append(info.DataPaths, p+"/meshviewer.json")
				}
			}
		}
	}

	if idx := strings.Index(text, "nodeInfos:"); idx >= 0 {
		sub := text[idx:]
		hrefRe := regexp.MustCompile(`href:"(https?://[^"]+/d/[^"]+)"`)
		if m := hrefRe.FindStringSubmatch(sub); len(m) > 1 {
			info.DashboardURL = m[1]
			if didx := strings.Index(m[1], "/d/"); didx > 0 {
				info.BaseURL = m[1][:didx]
			}
		}
	}

	if info.BaseURL == "" {
		if matches := grafanaURLPattern.FindAllString(text, 5); len(matches) > 0 {
			for _, m := range matches {
				m = strings.TrimRight(m, "\"'>,;)")
				if strings.Contains(m, "grafana") {
					if idx := strings.Index(m, "/d/"); idx > 0 {
						info.BaseURL = m[:idx]
					} else if idx := strings.Index(m, "/render"); idx > 0 {
						info.BaseURL = m[:idx]
					} else {
						info.BaseURL = m
					}
					break
				}
			}
		}
	}

	return info
}

func extractGrafanaFromConfig(body []byte) GrafanaInfo {
	var cfg map[string]interface{}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return GrafanaInfo{}
	}

	var info GrafanaInfo

	if dataPath, ok := cfg["dataPath"]; ok {
		if arr, ok := dataPath.([]interface{}); ok {
			for _, p := range arr {
				if s, ok := p.(string); ok && strings.HasPrefix(s, "http") {
					s = strings.TrimSuffix(s, "/")
					info.DataPaths = append(info.DataPaths, s+"/meshviewer.json")
				}
			}
		}
	}

	if nodeInfos, ok := cfg["nodeInfos"]; ok {
		if arr, ok := nodeInfos.([]interface{}); ok {
			for _, item := range arr {
				m, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				href, _ := m["href"].(string)
				if href == "" {
					continue
				}
				if strings.Contains(href, "/d/") || strings.Contains(href, "grafana") {
					if info.DashboardURL == "" {
						info.DashboardURL = href
					}
					if idx := strings.Index(href, "/d/"); idx > 0 {
						info.BaseURL = href[:idx]
					}
				}
			}
		}
	}

	if info.BaseURL != "" {
		return info
	}

	grafanaKeys := []string{"grafana", "grafanaApi", "statisticsApi", "siteStatistics"}
	for _, key := range grafanaKeys {
		val, ok := cfg[key]
		if !ok {
			continue
		}
		switch v := val.(type) {
		case string:
			if strings.HasPrefix(v, "http") {
				info.BaseURL = strings.TrimSuffix(v, "/")
				return info
			}
		case map[string]interface{}:
			if u, ok := v["url"].(string); ok && strings.HasPrefix(u, "http") {
				info.BaseURL = strings.TrimSuffix(u, "/")
				return info
			}
		}
	}

	if base := deepScanForGrafana(cfg); base != "" {
		info.BaseURL = base
		return info
	}

	return info
}

func deepScanForGrafana(v interface{}) string {
	switch val := v.(type) {
	case string:
		if strings.HasPrefix(val, "http") && strings.Contains(val, "grafana") {
			if idx := strings.Index(val, "/d/"); idx > 0 {
				return val[:idx]
			}
			if idx := strings.Index(val, "/dashboard"); idx > 0 {
				return val[:idx]
			}
			return strings.TrimSuffix(val, "/")
		}
	case map[string]interface{}:
		for _, sub := range val {
			if r := deepScanForGrafana(sub); r != "" {
				return r
			}
		}
	case []interface{}:
		for _, sub := range val {
			if r := deepScanForGrafana(sub); r != "" {
				return r
			}
		}
	}
	return ""
}

func discoverDatasource(client *http.Client, info GrafanaInfo) GrafanaInfo {
	probeClient := &http.Client{Timeout: 8 * time.Second}
	dsURL := strings.TrimSuffix(info.BaseURL, "/") + "/api/datasources"

	req, err := http.NewRequest("GET", dsURL, nil)
	if err != nil {
		return info
	}
	req.Header.Set("User-Agent", "freifunk-map-modern/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := probeClient.Do(req)
	if err != nil {
		return info
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		return info
	}

	var datasources []struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		Type      string `json:"type"`
		Database  string `json:"database"`
		IsDefault bool   `json:"isDefault"`
		JsonData  struct {
			DBName string `json:"dbName"`
		} `json:"jsonData"`
	}
	if err := json.Unmarshal(body, &datasources); err != nil {
		return info
	}

	for _, ds := range datasources {
		if ds.Type != "influxdb" {
			continue
		}
		nameLower := strings.ToLower(ds.Name)
		dbName := ds.Database
		if dbName == "" {
			dbName = ds.JsonData.DBName
		}
		dbLower := strings.ToLower(dbName)
		if strings.Contains(nameLower, "yanic") || strings.Contains(dbLower, "yanic") {
			info.DatasourceID = ds.ID
			info.Database = dbName
			return info
		}
	}

	for _, ds := range datasources {
		if ds.Type == "influxdb" && ds.IsDefault {
			dbName := ds.Database
			if dbName == "" {
				dbName = ds.JsonData.DBName
			}
			info.DatasourceID = ds.ID
			info.Database = dbName
			return info
		}
	}

	for _, ds := range datasources {
		if ds.Type == "influxdb" {
			dbName := ds.Database
			if dbName == "" {
				dbName = ds.JsonData.DBName
			}
			info.DatasourceID = ds.ID
			info.Database = dbName
			return info
		}
	}

	return info
}
