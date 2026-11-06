package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/freifunkMUC/freifunk-map-modern/internal/config"
)

func TestContactFollowsConfig(t *testing.T) {
	for _, show := range []bool{false, true} {
		s := New(&config.Config{ShowContact: show})
		snap := s.ProcessData(&MeshviewerData{Nodes: []RawNode{{NodeID: "a", Hostname: "h", Owner: "x@example.org"}}})
		out, err := json.Marshal(snap.NodeList)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(out), "x@example.org"); got != show {
			t.Errorf("showContact=%v: contact in output = %v", show, got)
		}
	}
}
