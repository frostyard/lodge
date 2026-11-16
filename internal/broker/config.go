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
	// Enabled starts the broker. It is on unless the member turns it off,
	// and a change takes effect at the next start.
	Enabled bool `json:"enabled"`
}

// LoadConfig reads the broker member of the configuration file at path. The
// broker is on by default: a missing file, a missing member, or a member
// without "enabled" is a broker that is on, and only "enabled": false turns
// it off.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{Enabled: true}, nil
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
		return Config{Enabled: true}, nil
	}
	var member struct {
		// Enabled is nil while the member names no setting: the broker is
		// then on.
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(file.Broker, &member, json.RejectUnknownMembers(true)); err != nil {
		return Config{}, fmt.Errorf("%s: broker: %w", path, err)
	}
	c := Config{Enabled: member.Enabled == nil || *member.Enabled}
	return c, nil
}
