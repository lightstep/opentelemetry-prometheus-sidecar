package telemetry

import (
	"context"
	"strings"

	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/number"
	export "go.opentelemetry.io/otel/sdk/export/metric"
	"go.opentelemetry.io/otel/sdk/export/metric/aggregation"
	"go.opentelemetry.io/otel/sdk/metric/aggregator/sum"
)

// copyToCounterProcessor copies every Histogram aggregation in the metrics
// export pipeline to a corresponding counter, which simplifies observability
// in some settings.
// TODO: Remove this after OTLP histogram support is more proven.
type copyToCounterProcessor struct {
	export.Checkpointer

	// Note: The metric controller and registry make it impossible
	// to directly access the registry; this is a workaround.
	// Probably OTel-Go's controller should let you provide a
	// registry for this sort of use-case.
	counters map[string]*metric.Descriptor
}

var _ export.Checkpointer

func newCopyToCounterProcessor(ckpter export.Checkpointer) export.Checkpointer {
	return &copyToCounterProcessor{
		Checkpointer: ckpter,
		counters:     map[string]*metric.Descriptor{},
	}
}

func (c *copyToCounterProcessor) Process(accum export.Accumulation) error {
	desc := accum.Descriptor()

	// TODO: Move this to a test.
	if !strings.HasPrefix(desc.Name(), "sidecar.") &&
		!strings.HasPrefix(desc.Name(), "runtime.") &&
		!strings.HasPrefix(desc.Name(), "system.") &&
		!strings.HasPrefix(desc.Name(), "process.") {
		return errors.Errorf("disallowed metric name prefix for sidecar: %v", desc.Name())
	}

	// Process the original accumulation first.
	if err := c.Checkpointer.Process(accum); err != nil {
		return err
	}
	// If it is not a value recorder, return.
	if desc.InstrumentKind() != metric.ValueRecorderInstrumentKind {
		return nil
	}

	histCount := accum.Aggregator().(aggregation.Count)

	value, err := histCount.Count()
	if err != nil {
		return err
	}

	// Look up the corresponding counter.
	cname := desc.Name() + ".count"
	cdesc, ok := c.counters[cname]
	if !ok {
		d := metric.NewDescriptor(
			cname,
			metric.CounterInstrumentKind,
			number.Int64Kind,
			metric.WithUnit(desc.Unit()),
			metric.WithDescription(desc.Description()),
		)
		cdesc = &d
		c.counters[cname] = cdesc
	}

	// Output a sum aggregator of the count.
	cnt := &sum.New(1)[0]
	cnt.Update(context.Background(), number.NewInt64Number(int64(value)), cdesc)
	return c.Checkpointer.Process(
		export.NewAccumulation(
			cdesc,
			accum.Labels(),
			accum.Resource(),
			cnt,
		),
	)
}
