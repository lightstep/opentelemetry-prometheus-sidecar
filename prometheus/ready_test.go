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

func TestReadySpecificInterval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	fs := promtest.NewFakePrometheus()
	fs.SetIntervals() // None set

	const interval = 79 * time.Second

	rc := fs.ReadyConfig()
	rc.LongestInterval = interval

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

	err := WaitForReady(context.Background(), rc)

	require.NoError(t, err)
}

func TestReadySpecificIntervalWait(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	fs := promtest.NewFakePrometheus()
	fs.SetIntervals(19 * time.Second) // Not the one we want

	const interval = 79 * time.Second

	rc := fs.ReadyConfig()
	rc.LongestInterval = interval

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultHealthCheckTimeout*4/3)
	defer cancel()
	err := WaitForReady(ctx, rc)

	require.Error(t, err)
	require.Equal(t, context.DeadlineExceeded, err)
}
