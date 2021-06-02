package common

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-kit/kit/log"
	"go.opentelemetry.io/otel/metric"
	"strings"
	"testing"
)

func TestFailingSetMaxMetricsLogged(t *testing.T) {
	buf := bytes.NewBuffer(make([]byte, 0, 2048))
	fs := NewFailingSet(log.NewJSONLogger(buf))

	for i := 0; i < 100; i++ {
		fs.Set("metadata_not_found", fmt.Sprintf("%d", i))
	}

	// force the short map to be nil, so we don't record any metric.
	fs.short = nil
	ignored := metric.Int64ObserverResult{}

	fs.observe(context.Background(), ignored)

	var m map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &m)
	if err != nil {
		t.Fatalf("expected no error when unmarshalling json log, but got %s", err.Error())
	}

	names := strings.Split(m["names"].(string), " ")
	l := len(names)
	if l != 50 {
		t.Fatalf("expected 50 names, got %d instead: %v", l, m["names"])
	}
}
