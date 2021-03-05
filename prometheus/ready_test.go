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
	fs.SetIntervals(30 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultHealthCheckTimeout*4/3)
	defer cancel()

	require.NoError(t, WaitForReady(ctx, cancel, fs.ReadyConfig()))
}

func TestInvalidVersion(t *testing.T) {
	fs := promtest.NewFakePrometheusWithVersion("2.1.0")

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultHealthCheckTimeout*4/3)
	defer cancel()
	err := WaitForReady(ctx, cancel, fs.ReadyConfig())
	require.Error(t, err)
	require.Equal(t, context.Canceled, err)
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
	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultHealthCheckTimeout*4/3)
	defer cancel()
	require.NoError(t, WaitForReady(ctx, cancel, fs.ReadyConfig()))
}

func TestNotReady(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	fs := promtest.NewFakePrometheus()
	fs.SetReady(false)

	ctx, cancel := context.WithTimeout(context.Background(), 2*config.DefaultHealthCheckTimeout)
	defer cancel()
	err := WaitForReady(ctx, cancel, fs.ReadyConfig())
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
	err = WaitForReady(ctx, cancel, config.PromReady{
		Logger:  logger,
		PromURL: tu,
	})
	require.Error(t, err)
}

func TestReadyCancel(t *testing.T) {
	fs := promtest.NewFakePrometheus()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate
	err := WaitForReady(ctx, cancel, fs.ReadyConfig())

	require.Error(t, err)
	require.Equal(t, context.Canceled, err)
}

func TestReadySpecificInterval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	fs := promtest.NewFakePrometheus()
	fs.SetIntervals() // None set

	const interval = 79 * time.Second

	rc := fs.ReadyConfig()
	rc.ScrapeIntervals = []time.Duration{time.Minute, interval}

	go func() {
		time.Sleep(1 * time.Second)
		fs.SetIntervals(20 * time.Second)

		time.Sleep(1 * time.Second)
		fs.SetIntervals(20*time.Second, 40*time.Second)

		time.Sleep(1 * time.Second)
		fs.SetIntervals(20*time.Second, 40*time.Second, 60*time.Second)

		time.Sleep(1 * time.Second)
		fs.SetIntervals(20*time.Second, 40*time.Second, 60*time.Second, interval)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	err := WaitForReady(ctx, cancel, rc)

	require.NoError(t, err)
}

func TestReadySpecificIntervalWait(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	fs := promtest.NewFakePrometheus()
	fs.SetIntervals(19 * time.Second) // Not the one we want

	rc := fs.ReadyConfig()
	rc.ScrapeIntervals = []time.Duration{79 * time.Second, 19 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultHealthCheckTimeout*4/3)
	defer cancel()
	err := WaitForReady(ctx, cancel, rc)

	require.Error(t, err)
	require.Equal(t, context.DeadlineExceeded, err)
}
