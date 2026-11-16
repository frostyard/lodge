package broker

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
)

// Config is the broker member of lodge's configuration
// (docs/specs/lodge-http.md, The broker).
type Config struct {
	// Enabled starts the broker. A change takes effect at the next start.
	Enabled bool `json:"enabled"`
}

// LoadConfig reads the broker member of the configuration file at path. A
// missing file, or one without the member, is a broker that is off.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	var file struct {
		Broker jsontext.Value `json:"broker"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if len(file.Broker) == 0 {
		return Config{}, nil
	}
	var c Config
	if err := json.Unmarshal(file.Broker, &c, json.RejectUnknownMembers(true)); err != nil {
		return Config{}, fmt.Errorf("%s: broker: %w", path, err)
	}
	return c, nil
}
