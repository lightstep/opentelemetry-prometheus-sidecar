package prometheus

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/promtest"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/stretchr/testify/require"
)

var logger = telemetry.DefaultLogger()

func TestReady(t *testing.T) {
	fs := promtest.NewFakePrometheus()
	fs.SetReady(true)
	fs.SetIntervals(30)

	require.NoError(t, WaitForReady(context.Background(), fs.ReadyConfig()))
}

func TestSlowStart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	fs := promtest.NewFakePrometheus()
	fs.SetReady(true)
	fs.SetIntervals()

	go func() {
		time.Sleep(time.Second * 3)
		fs.SetIntervals(30)
	}()

	require.NoError(t, WaitForReady(context.Background(), fs.ReadyConfig()))
}

func TestNotReady(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	fs := promtest.NewFakePrometheus()
	fs.SetReady(false)

	ctx, cancel := context.WithTimeout(context.Background(), 2*config.DefaultHealthCheckTimeout)
	defer cancel()
	err := WaitForReady(ctx, fs.ReadyConfig())
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
	err = WaitForReady(ctx, config.PromReady{
		Logger:  logger,
		PromURL: tu,
	})
	require.Error(t, err)
}

func TestReadyCancel(t *testing.T) {
	fs := promtest.NewFakePrometheus()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate
	err := WaitForReady(ctx, fs.ReadyConfig())

	require.Error(t, err)
	require.Equal(t, context.Canceled, err)
}
