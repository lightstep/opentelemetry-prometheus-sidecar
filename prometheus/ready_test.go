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
	fs := promtest.NewFakePrometheus(promtest.Config{})
	fs.SetReady(true)
	fs.SetIntervals(30 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultHealthCheckTimeout*4/3)
	defer cancel()

	require.NoError(t, NewMonitor(fs.ReadyConfig()).WaitForReady(ctx, cancel))
}

func TestInvalidVersion(t *testing.T) {
	fs := promtest.NewFakePrometheus(promtest.Config{Version: "2.1.0"})

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultHealthCheckTimeout*4/3)
	defer cancel()
	err := NewMonitor(fs.ReadyConfig()).WaitForReady(ctx, cancel)
	require.Error(t, err)
	require.Equal(t, context.Canceled, err)
}

func TestSlowStart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	fs := promtest.NewFakePrometheus(promtest.Config{})
	fs.SetReady(true)
	fs.SetIntervals()

	go func() {
		time.Sleep(time.Second * 3)
		fs.SetIntervals(30)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultHealthCheckTimeout*4/3)
	defer cancel()
	require.NoError(t, NewMonitor(fs.ReadyConfig()).WaitForReady(ctx, cancel))
}

func TestNotReady(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	fs := promtest.NewFakePrometheus(promtest.Config{})
	fs.SetReady(false)

	ctx, cancel := context.WithTimeout(context.Background(), 2*config.DefaultHealthCheckTimeout)
	defer cancel()
	err := NewMonitor(fs.ReadyConfig()).WaitForReady(ctx, cancel)
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
	err = NewMonitor(config.PromReady{
		Logger:  logger,
		PromURL: tu,
	}).WaitForReady(ctx, cancel)
	require.Error(t, err)
}

func TestReadyCancel(t *testing.T) {
	fs := promtest.NewFakePrometheus(promtest.Config{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate
	err := NewMonitor(fs.ReadyConfig()).WaitForReady(ctx, cancel)

	require.Error(t, err)
	require.Equal(t, context.Canceled, err)
}

func TestReadySpecificInterval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	fs := promtest.NewFakePrometheus(promtest.Config{})
	fs.SetIntervals() // None set
	fs.SetPromConfigYaml(`
scrape_configs:
  - job_name: 'long'
    scrape_interval: 79s
    static_configs:
    - targets: ['localhost:18000']
`)

	const interval = 79 * time.Second

	checker := NewMonitor(fs.ReadyConfig())

	go func() {
		time.Sleep(5 * time.Second)
		fs.SetIntervals(interval)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	err := checker.WaitForReady(ctx, cancel)

	require.NoError(t, err)
}

func TestReadySpecificNoInterval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	fs := promtest.NewFakePrometheus(promtest.Config{})
	fs.SetIntervals() // None set
	fs.SetPromConfigYaml("")

	checker := NewMonitor(fs.ReadyConfig())

	go func() {
		time.Sleep(5 * time.Second)
		fs.SetIntervals(88 * time.Second) // unknown to the sidecar; not default
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := checker.WaitForReady(ctx, cancel)

	require.NoError(t, err)
}

func TestReadySpecificIntervalWait(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	fs := promtest.NewFakePrometheus(promtest.Config{})
	fs.SetIntervals(19 * time.Second) // Not the one we want

	// Configure 19s and 79s intervals
	fs.SetPromConfigYaml(`
scrape_configs:
  - job_name: 'short'
    scrape_interval: 19s
    static_configs:
    - targets: ['localhost:18001']
  - job_name: 'long'
    scrape_interval: 79s
    static_configs:
    - targets: ['localhost:18000']
`)

	checker := NewMonitor(fs.ReadyConfig())
	checker.cfg.StartupDelayEffectiveStartTime = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultHealthCheckTimeout*4/3)
	defer cancel()
	err := checker.WaitForReady(ctx, cancel)

	require.Error(t, err)
	require.Equal(t, context.DeadlineExceeded, err)
}

func TestStartupDelayEffectiveStartTime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	fs := promtest.NewFakePrometheus(promtest.Config{})
	fs.SetIntervals(19 * time.Second) // Not the one we want

	// Configure 19s and 79s intervals
	fs.SetPromConfigYaml(`
scrape_configs:
  - job_name: 'short'
    scrape_interval: 19s
    static_configs:
    - targets: ['localhost:18001']
  - job_name: 'long'
    scrape_interval: 79s
    static_configs:
    - targets: ['localhost:18000']
`)

	checker := NewMonitor(fs.ReadyConfig())
	checker.cfg.StartupDelayEffectiveStartTime = time.Now().Add(-(79*time.Second + config.DefaultScrapeIntervalWaitPeriod))

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultHealthCheckTimeout*4/3)
	defer cancel()
	err := checker.WaitForReady(ctx, cancel)

	require.NoError(t, err)

}

func TestReadyConfigParseError(t *testing.T) {
	fs := promtest.NewFakePrometheus(promtest.Config{})
	fs.SetIntervals() // None set
	fs.SetPromConfigYaml("sdf")

	checker := NewMonitor(fs.ReadyConfig())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := checker.WaitForReady(ctx, cancel)

	require.Error(t, err)
	require.Equal(t, context.Canceled, err)
}
