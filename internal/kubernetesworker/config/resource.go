package config

import (
	_ "embed"
	"encoding/json"
)

// ResourceDescriptor is generated from the served ApplicationDefinition so
// webhook matching cannot quietly target a guessed plural.
type ResourceDescriptor struct {
	Group    string `json:"group"`
	Version  string `json:"version"`
	Kind     string `json:"kind"`
	Resource string `json:"resource"`
}

//go:embed resource.json
var resourceJSON []byte

func PoolResource() ResourceDescriptor {
	var descriptor ResourceDescriptor
	if err := json.Unmarshal(resourceJSON, &descriptor); err != nil {
		panic(err)
	}
	return descriptor
}
