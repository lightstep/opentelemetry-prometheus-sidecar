/*
Copyright 2018 Google Inc.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package retrieval

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/tail"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry/doevery"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/tsdb/fileutil"
	"github.com/prometheus/prometheus/tsdb/record"
	"github.com/prometheus/prometheus/tsdb/wal"
	"go.opentelemetry.io/otel/metric"
)

var (
	samplesProcessed = sidecar.OTelMeterMust.NewInt64ValueRecorder(
		config.ProcessedMetric,
		metric.WithDescription("Number of WAL samples processed in a batch"),
	)

	samplesProduced = sidecar.OTelMeterMust.NewInt64Counter(
		config.ProducedMetric,
		metric.WithDescription("Number of Metric samples produced"),
	)
)

type MetadataGetter interface {
	Get(ctx context.Context, job, instance, metric string) (*metadata.Entry, error)
}

// NewPrometheusReader is the PrometheusReader constructor
func NewPrometheusReader(
	logger log.Logger,
	walDirectory string,
	tailer *tail.Tailer,
	filters [][]*labels.Matcher,
	metricRenames map[string]string,
	metadataGetter MetadataGetter,
	appender Appender,
	metricsPrefix string,
	maxPointAge time.Duration,
	extraLabels labels.Labels,
) *PrometheusReader {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &PrometheusReader{
		appender:             appender,
		logger:               logger,
		tailer:               tailer,
		filters:              filters,
		walDirectory:         walDirectory,
		metadataGetter:       metadataGetter,
		progressSaveInterval: time.Minute,
		metricRenames:        metricRenames,
		metricsPrefix:        metricsPrefix,
		maxPointAge:          maxPointAge,
		extraLabels:          extraLabels,
	}
}

type PrometheusReader struct {
	logger               log.Logger
	walDirectory         string
	tailer               *tail.Tailer
	filters              [][]*labels.Matcher
	metricRenames        map[string]string
	metadataGetter       MetadataGetter
	appender             Appender
	progressSaveInterval time.Duration
	metricsPrefix        string
	maxPointAge          time.Duration
	extraLabels          labels.Labels
}

func (r *PrometheusReader) Next() {
	r.tailer.Next()
}

func (r *PrometheusReader) CurrentSegment() int {
	return r.tailer.CurrentSegment()
}

func (r *PrometheusReader) Run(ctx context.Context, startOffset int) error {
	level.Info(r.logger).Log("msg", "starting Prometheus reader")

	seriesCache := newSeriesCache(
		r.logger,
		r.walDirectory,
		r.filters,
		r.metricRenames,
		r.metadataGetter,
		r.metricsPrefix,
		r.extraLabels,
	)
	go seriesCache.run(ctx)

	builder := &sampleBuilder{
		series:      seriesCache,
		maxPointAge: r.maxPointAge,
	}

	// NOTE(fabxc): wrap the tailer into a buffered reader once we become concerned
	// with performance. The WAL reader will do a lot of tiny reads otherwise.
	// This is also the reason for the series cache dealing with "maxSegment" hints
	// for series rather than precise ones.
	var (
		started  = false
		skipped  = 0
		reader   = wal.NewReader(r.tailer)
		err      error
		lastSave time.Time
		samples  []record.RefSample
		series   []record.RefSeries
	)
Outer:
	for reader.Next() {
		offset := r.tailer.Offset()
		rec := reader.Record()

		if offset > startOffset && time.Since(lastSave) > r.progressSaveInterval {
			if err := SaveProgressFile(r.walDirectory, offset); err != nil {
				level.Error(r.logger).Log("msg", "saving progress failed", "err", err)
			} else {
				lastSave = time.Now()
			}
		}
		var decoder record.Decoder

		switch decoder.Type(rec) {
		case record.Series:
			series, err = decoder.Series(rec, series[:0])
			if err != nil {
				level.Error(r.logger).Log("msg", "decode series", "err", err)
				continue
			}
			for _, s := range series {
				err = seriesCache.set(ctx, s.Ref, s.Labels, r.tailer.CurrentSegment())
				if err != nil {
					doevery.TimePeriod(config.DefaultNoisyLogPeriod, func() {
						level.Error(r.logger).Log(
							"msg", "update series cache",
							"err", err,
						)
					})
				}
			}
		case record.Samples:
			// Skip sample records before the the boundary offset.
			if offset < startOffset {
				skipped++
				continue
			}
			if !started {
				level.Info(r.logger).Log("msg", "reached first record after start offset",
					"start_offset", startOffset, "skipped_records", skipped)
				started = true
			}
			samples, err = decoder.Samples(rec, samples[:0])
			if err != nil {
				level.Error(r.logger).Log("decode samples", "err", err)
				continue
			}
			processed, produced := len(samples), 0

			for len(samples) > 0 {
				select {
				case <-ctx.Done():
					break Outer
				default:
				}

				outputSample, hash, newSamples, err := builder.next(ctx, samples)

				if len(samples) == len(newSamples) {
					// Note: There are a few code paths in `builder.next()`
					// where it's easier to fall through to this than to be
					// sure the samples list becomes shorter by at least 1.
					samples = samples[1:]
				} else {
					samples = newSamples
				}
				if err != nil {
					// Note: This case is poorly monitored (LS-22396):
					doevery.TimePeriod(config.DefaultNoisyLogPeriod, func() {
						level.Warn(r.logger).Log("msg", "failed to build sample", "err", err)
					})
					continue
				}
				if outputSample == nil {
					// Note: This case is poorly monitored (LS-22396): some of these
					// cases are timestamp resets, which are ordinary, but can't be
					// monitored either.
					continue
				}
				r.appender.Append(ctx, hash, outputSample)
				produced++
			}

			sidecar.OTelMeter.RecordBatch(
				ctx,
				nil,
				samplesProcessed.Measurement(int64(processed)),
				samplesProduced.Measurement(int64(produced)),
			)

		case record.Tombstones:
		default:
			level.Warn(r.logger).Log("msg", "unknown WAL record type")
		}
	}
	level.Info(r.logger).Log("msg", "done processing WAL")
	return reader.Err()
}

const (
	progressFilename     = "opentelemetry_sidecar.json"
	progressBufferMargin = 512 * 1024
)

// progress defines the JSON object of the progress file.
type progress struct {
	// Approximate WAL offset of last synchronized records in bytes.
	Offset int `json:"offset"`
}

// ReadProgressFile reads the progress file in the given directory and returns
// the saved offset.
func ReadProgressFile(dir string) (offset int, err error) {
	b, err := ioutil.ReadFile(filepath.Join(dir, progressFilename))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var p progress
	if err := json.Unmarshal(b, &p); err != nil {
		return 0, err
	}
	return p.Offset, nil
}

// SaveProgressFile saves a progress file with the given offset in directory.
func SaveProgressFile(dir string, offset int) error {
	// Adjust offset to account for buffered records that possibly haven't been
	// written yet.
	b, err := json.Marshal(progress{Offset: offset - progressBufferMargin})
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, progressFilename+".tmp")
	if err := ioutil.WriteFile(tmp, b, 0666); err != nil {
		return err
	}
	if err := fileutil.Rename(tmp, filepath.Join(dir, progressFilename)); err != nil {
		return err
	}
	return nil
}

// copyLabels copies a slice of labels.  The caller will mutate the
// copy, otherwise the types are the same.  Note that the code could
// be restructured to avoid this copy.
func copyLabels(input labels.Labels) labels.Labels {
	output := make(labels.Labels, len(input))
	copy(output, input)
	return output
}

func hashSeries(s tsDesc) uint64 {
	const sep = '\xff'
	h := hashNew()

	h = hashAdd(h, s.Name)
	h = hashAddByte(h, sep)

	// Both lists are sorted
	for _, l := range s.Labels {
		h = hashAddByte(h, sep)
		h = hashAdd(h, l.Name)
		h = hashAddByte(h, sep)
		h = hashAdd(h, l.Value)
	}
	h = hashAddByte(h, sep)
	for _, l := range s.Resource {
		h = hashAddByte(h, sep)
		h = hashAdd(h, l.Name)
		h = hashAddByte(h, sep)
		h = hashAdd(h, l.Value)
	}
	return h
}

func exponential(d time.Duration) time.Duration {
	const (
		min = 10 * time.Millisecond
		max = 2 * time.Second
	)
	d *= 2
	if d < min {
		d = min
	}
	if d > max {
		d = max
	}
	return d
}
