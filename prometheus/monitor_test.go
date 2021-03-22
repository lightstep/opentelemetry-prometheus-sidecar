package prometheus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/stretchr/testify/require"
)

func TestMonitorScrape(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte(`
# HELP http_requests_total The total number of HTTP requests.
# TYPE http_requests_total counter
http_requests_total{method="post",code="200"} 1027 1395066363000
http_requests_total{method="post",code="400"}    3 1395066363000

# HELP blah blah
# TYPE utilization gauge
utilization{} 123
`))
		require.NoError(t, err)
	}))

	tu, err := url.Parse(ts.URL)
	require.NoError(t, err)

	m := NewMonitor(config.PromReady{
		Logger:  telemetry.DefaultLogger(),
		PromURL: tu,
	})

	ctx := context.Background()
	res, err := m.GetMetrics(ctx)
	require.NoError(t, err)

	// Positive examples
	require.Equal(t, 1027.0, res.Counter("http_requests_total").
		For(labels.FromStrings("method", "post", "code", "200")))

	require.Equal(t, 3.0, res.Counter("http_requests_total").
		For(labels.FromStrings("method", "post", "code", "400")))

	require.Equal(t, 123.0, res.Gauge("utilization").
		For(labels.FromStrings()))

	// Negative examples
	require.Equal(t, 0.0, res.Counter("http_requests_total").
		For(labels.FromStrings("method", "post", "code", "400", "user", "nobody")))

	require.Equal(t, 0.0, res.Counter("http_requests_total").
		For(labels.FromStrings("method", "post", "code", "500")))

	require.Equal(t, 0.0, res.Counter("other_requests_total").
		For(labels.FromStrings("method", "post", "code", "400")))

}
