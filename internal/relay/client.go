package relay

import "errors"

// ClientConfig contains only the selected connection and local authentication.
// It contains no upstream credentials.
type ClientConfig struct {
	BaseURL  string
	LocalKey string
	Models   []string
}

// LoadClientConfig reads the connection without opening upstream key files.
func LoadClientConfig(path string) (*ClientConfig, error) {
	raw, err := readConfig(path)
	if err != nil {
		return nil, err
	}
	token, err := loadSecret(path, raw.AccessTokenFile)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(raw.Models))
	for id, entry := range raw.Models {
		if _, ok := raw.Upstreams[entry.Upstream]; id == "" || !ok {
			return nil, errors.New("empty model ID or missing upstream reference")
		}
		models = append(models, id)
	}
	return &ClientConfig{BaseURL: "http://" + raw.Listen, LocalKey: token, Models: models}, nil
}
