package common

import "github.com/prometheus/prometheus/pkg/textparse"

type TargetMetadataAPIResponse struct {
	Status    string              `json:"status"`
	Data      []APITargetMetadata `json:"data,omitempty"`
	Error     string              `json:"error,omitempty"`
	ErrorType string              `json:"errorType,omitempty"`
	Warnings  []string            `json:"warnings,omitempty"`
}

type APITargetMetadata struct {
	// We do not decode the target information.
	Metric string               `json:"metric"`
	Help   string               `json:"help"`
	Type   textparse.MetricType `json:"type"`
}

type MetadataAPIResponse struct {
	Status    string                   `json:"status"`
	Data      map[string][]APIMetadata `json:"data,omitempty"`
	Error     string                   `json:"error,omitempty"`
	ErrorType string                   `json:"errorType,omitempty"`
	Warnings  []string                 `json:"warnings,omitempty"`
}

type APIMetadata struct {
	Help   string               `json:"help"`
	Type   textparse.MetricType `json:"type"`
}

type ConfigAPIResponse struct {
	Status    string    `json:"status"`
	Data      APIConfig `json:"data,omitempty"`
	ErrorType string    `json:"errorType,omitempty"`
	Error     string    `json:"error,omitempty"`
	Warnings  []string  `json:"warnings,omitempty"`
}

type APIConfig struct {
	YAML string `json:"yaml"`
}
