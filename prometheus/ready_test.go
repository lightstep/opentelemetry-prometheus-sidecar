package prometheus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/stretchr/testify/require"
)

var logger = telemetry.DefaultLogger()

func TestReady(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tu, err := url.Parse(ts.URL)
	require.NoError(t, err)

	require.NoError(t, WaitForReady(context.Background(), logger, tu))
}

func TestNotReady(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))

	tu, err := url.Parse(ts.URL)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*config.DefaultHealthCheckTimeout)
	defer cancel()
	err = WaitForReady(ctx, logger, tu)
	require.Error(t, err)
	require.Equal(t, context.DeadlineExceeded, err)
}

func TestReadyFail(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	tu, err := url.Parse("http://127.0.0.1:9999/__notfound__")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*config.DefaultHealthCheckTimeout)
	defer cancel()
	err = WaitForReady(ctx, logger, tu)
	require.Error(t, err)
}

func TestReadyCancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tu, err := url.Parse(ts.URL)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate
	err = WaitForReady(ctx, logger, tu)
	require.Error(t, err)
	require.Equal(t, context.Canceled, err)
}
