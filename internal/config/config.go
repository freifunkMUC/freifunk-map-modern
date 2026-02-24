package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type TileLayer struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Attribution string `json:"attribution"`
	MaxZoom     int    `json:"maxZoom"`
}

type ExternalLink struct {
	Title string `json:"title"`
	Href  string `json:"href"`
}

type Config struct {
	Listen           string            `json:"listen"`
	SiteName         string            `json:"siteName"`
	DataURL          string            `json:"dataURL"`
	RefreshInterval  string            `json:"refreshInterval"`
	GrafanaURL       string            `json:"grafanaURL"`
	GrafanaDashboard string            `json:"grafanaDashboard"`
	GrafanaOrgId     int               `json:"grafanaOrgId"`
	MapCenter        [2]float64        `json:"mapCenter"`
	MapZoom          int               `json:"mapZoom"`
	TileLayers       []TileLayer       `json:"tileLayers"`
	DomainNames      map[string]string `json:"domainNames"`
	Links            []ExternalLink    `json:"links"`
	DevicePictureURL string            `json:"devicePictureURL"`
	EolInfoURL       string            `json:"eolInfoURL"`
	Federation       bool              `json:"federation"`

	// Parsed internally
	RefreshDuration time.Duration `json:"-"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	cfg := &Config{
		Listen:           ":8080",
		SiteName:         "Freifunk Map",
		RefreshInterval:  "60s",
		MapCenter:        [2]float64{48.1351, 11.5820},
		MapZoom:          10,
		GrafanaOrgId:     1,
		DevicePictureURL: "https://map.aachen.freifunk.net/pictures-svg/{MODEL}.svg",
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg.RefreshDuration, err = time.ParseDuration(cfg.RefreshInterval)
	if err != nil {
		cfg.RefreshDuration = 60 * time.Second
	}

	if cfg.DataURL == "" && !cfg.Federation {
		return nil, fmt.Errorf("dataURL is required in config (or set federation: true)")
	}

	if cfg.Federation && cfg.SiteName == "Freifunk Map" {
		cfg.SiteName = "Freifunk Federation Map"
	}

	if cfg.Federation {
		cfg.MapCenter = [2]float64{51.1657, 10.4515} // center of Germany
		if cfg.MapZoom == 10 {
			cfg.MapZoom = 6
		}
	}

	return cfg, nil
}
