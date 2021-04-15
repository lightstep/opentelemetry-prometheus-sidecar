package internal

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/go-kit/kit/log"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/prometheus"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/tail"
	"github.com/stretchr/testify/require"
)

type fakeTailer struct {
	readError error
	sizeError error
}

func (t *fakeTailer) Size() (int, error) {
	return 0, t.sizeError
}

func (t *fakeTailer) Next() {
}

func (t *fakeTailer) Offset() int {
	return 0
}

func (t *fakeTailer) Close() error {
	return nil
}

func (t *fakeTailer) CurrentSegment() int {
	return 0
}

func (t *fakeTailer) Read(b []byte) (int, error) {
	return 0, t.readError
}

func (t *fakeTailer) SetCurrentSegment(int) {
}

var _ tail.WalTailer = &fakeTailer{}

func TestStartComponents(t *testing.T) {
	// test that we only loop for err skip segment
	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	scfg := SidecarConfig{}
	scfg.Monitor = &prometheus.Monitor{}
	scfg.Logger = logger
	ctx := context.Background()
	tailer := fakeTailer{
		sizeError: errors.New("failed to get size"),
		readError: errors.New("failed to read"),
	}
	err := StartComponents(ctx, scfg, &tailer, 0)
	require.Error(t, err)

	tailer = fakeTailer{
		sizeError: tail.ErrSkipSegment,
		readError: errors.New("failed to read"),
	}
	err = StartComponents(ctx, scfg, &tailer, 0)
	require.Error(t, err)

	tailer = fakeTailer{
		readError: errors.New("failed to read"),
	}
	err = StartComponents(ctx, scfg, &tailer, 0)
	require.Error(t, err)

	tailer = fakeTailer{
		readError: tail.ErrSkipSegment,
	}
	err = StartComponents(ctx, scfg, &tailer, 0)
	require.Error(t, err)

}
