package common

import "github.com/prometheus/prometheus/pkg/textparse"

type APIResponse struct {
	Status    string        `json:"status"`
	Data      []APIMetadata `json:"data"`
	Error     string        `json:"error"`
	ErrorType string        `json:"errorType"`
}

type APIMetadata struct {
	// We do not decode the target information.
	Metric string               `json:"metric"`
	Help   string               `json:"help"`
	Type   textparse.MetricType `json:"type"`
}
