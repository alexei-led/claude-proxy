package relay

import (
	"encoding/json"
	"sort"
)

type catalogModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

func catalog(config *Config) []byte {
	entries := make([]catalogModel, 0, len(config.models))
	for id, entry := range config.models {
		name := entry.displayName
		if name == "" {
			name = id
		}
		entries = append(entries, catalogModel{ID: id, DisplayName: name})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	body, _ := json.Marshal(struct {
		Data    []catalogModel `json:"data"`
		HasMore bool           `json:"has_more"`
	}{Data: entries})
	return body
}
