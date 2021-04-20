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
	"github.com/lightstep/opentelemetry-prometheus-sidecar/common"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/tail"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry/doevery"
	promconfig "github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/tsdb/fileutil"
	"github.com/prometheus/prometheus/tsdb/record"
	"github.com/prometheus/prometheus/tsdb/wal"
	"go.opentelemetry.io/otel/metric"
	metric_pb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

var (
	pointsProduced = sidecar.OTelMeterMust.NewInt64Counter(
		config.ProducedPointsMetric,
		metric.WithDescription("Number of Metric points produced"),
	)

	seriesDefined = sidecar.OTelMeterMust.NewInt64Counter(
		config.SeriesDefinedMetric,
		metric.WithDescription("Number of Metric series defined"),
	)
)

type MetadataGetter interface {
	Get(ctx context.Context, job, instance, metric string) (*config.MetadataEntry, error)
}

// NewPrometheusReader is the PrometheusReader constructor
func NewPrometheusReader(
	logger log.Logger,
	walDirectory string,
	tailer tail.WalTailer,
	filters [][]*labels.Matcher,
	metricRenames map[string]string,
	metadataGetter MetadataGetter,
	appender Appender,
	metricsPrefix string,
	maxPointAge time.Duration,
	scrapeConfig []*promconfig.ScrapeConfig,
	failingReporter common.FailingReporter,
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
		scrapeConfig:         scrapeConfig,
		failingReporter:      failingReporter,
	}
}

type PrometheusReader struct {
	logger               log.Logger
	walDirectory         string
	tailer               tail.WalTailer
	filters              [][]*labels.Matcher
	metricRenames        map[string]string
	metadataGetter       MetadataGetter
	appender             Appender
	progressSaveInterval time.Duration
	metricsPrefix        string
	maxPointAge          time.Duration
	scrapeConfig         []*promconfig.ScrapeConfig
	failingReporter      common.FailingReporter
}

func (r *PrometheusReader) Next() {
	r.tailer.Next()
}

func (r *PrometheusReader) CurrentSegment() int {
	return r.tailer.CurrentSegment()
}

// getjobInstanceMap returns a string map for any job for which the instance
// label has been relabeled
func (r *PrometheusReader) getJobInstanceMap() map[string]string {
	jobInstanceMap := make(map[string]string)
	for _, config := range r.scrapeConfig {
		newInstanceLabel := ""
		for _, metricRelabel := range config.MetricRelabelConfigs {
			switch metricRelabel.Action {
			case "replace":
				for _, ln := range metricRelabel.SourceLabels {
					if string(ln) == "instance" {
						newInstanceLabel = metricRelabel.TargetLabel
					}
				}
			case "labeldrop":
				if metricRelabel.Regex.MatchString("instance") && len(newInstanceLabel) > 0 {
					jobInstanceMap[config.JobName] = newInstanceLabel
				}
			case "labelkeep":
				if !metricRelabel.Regex.MatchString("instance") && len(newInstanceLabel) > 0 {
					jobInstanceMap[config.JobName] = newInstanceLabel
				}
			default:
				// no other action required
			}
		}
	}
	return jobInstanceMap
}

func (r *PrometheusReader) Run(ctx context.Context, startOffset int) error {
	level.Info(r.logger).Log("msg", "starting Prometheus reader")
	jobInstanceMap := r.getJobInstanceMap()

	seriesCache := newSeriesCache(
		r.logger,
		r.walDirectory,
		r.filters,
		r.metricRenames,
		r.metadataGetter,
		r.metricsPrefix,
		jobInstanceMap,
		r.failingReporter,
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
		started         = false
		startupBypassed = 0
		reader          = wal.NewReader(r.tailer)
		err             error
		lastSave        time.Time
		samples         []record.RefSample
		series          []record.RefSeries
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
			success, failed := 0, 0
			for _, s := range series {
				err = seriesCache.set(ctx, s.Ref, s.Labels, r.tailer.CurrentSegment())
				if err == nil {
					success++
					continue
				}
				failed++
				doevery.TimePeriod(config.DefaultNoisyLogPeriod, func() {
					level.Error(r.logger).Log(
						"msg", "update series cache",
						"err", err,
					)
				})
			}

			if failed != 0 {
				common.DroppedSeries.Add(
					ctx,
					int64(failed),
					common.DroppedKeyReason.String("metadata"),
				)
			}
			seriesDefined.Add(ctx, int64(success))

		case record.Samples:
			// Skip sample records before the the boundary offset.
			if offset < startOffset {
				startupBypassed++
				continue
			}
			if !started {
				level.Info(r.logger).Log(
					"msg", "reached first record after start offset",
					"start_offset", startOffset,
					"bypassed_records", startupBypassed)
				started = true
			}
			samples, err = decoder.Samples(rec, samples[:0])
			if err != nil {
				level.Error(r.logger).Log("decode samples", "err", err)
				continue
			}
			produced, droppedPoints, skippedPoints := 0, 0, 0
			var outputs []*metric_pb.Metric

			for len(samples) > 0 {
				select {
				case <-ctx.Done():
					break Outer
				default:
				}

				outputSample, newSamples, err := builder.next(ctx, samples)

				if len(samples) == len(newSamples) {
					// Note: There are a few code paths in `builder.next()`
					// where it's easier to fall through to this than to be
					// sure the samples list becomes shorter by at least 1.
					samples = samples[1:]
				} else {
					samples = newSamples
				}
				if err != nil {
					droppedPoints++
					doevery.TimePeriod(config.DefaultNoisyLogPeriod, func() {
						level.Warn(r.logger).Log("msg", "failed to build sample", "err", err)
					})
					continue
				}
				if outputSample == nil {
					skippedPoints++
					continue
				}
				outputs = append(outputs, outputSample)
				produced++
			}

			for _, out := range r.reduceSamples(outputs) {
				r.appender.Append(ctx, out)
			}

			if droppedPoints != 0 {
				common.DroppedPoints.Add(ctx, int64(droppedPoints),
					common.DroppedKeyReason.String("metadata"),
				)
			}
			if skippedPoints != 0 {
				common.SkippedPoints.Add(ctx, int64(skippedPoints))
			}

			pointsProduced.Add(ctx, int64(produced))

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

func (r *PrometheusReader) reduceSamples(samples []*metric_pb.Metric) []SizedMetric {
	const batchLimit = 1 << 12 // TODO

	smap := map[string][]*metric_pb.Metric{}

	for _, s := range samples {
		smap[s.Name] = append(smap[s.Name], s)
	}

	var res []SizedMetric

	for name, pms := range smap {
		total := len(name)

		for len(pms) > 0 {

			pm0 := pms[0]
			pms = pms[1:]

			for len(pms) > 0 {
				first := pms[0]
				sz := proto.Size(first)

				if total+sz > batchLimit {
					break
				}

				if !combine(pm0, first) {
					break
				}

				total += sz
				pms = pms[1:]
			}

			res = append(res, SizedMetric{
				Metric: pm0,
				Size:   total, // Note: approximate is OK.
			})
		}
	}

	return res
}

// combine assumes that each Metric contains one data point and tries
// to combine a same-name pair into one.  It assumes Prometheus-to-OTLP
// conversion was done and there is no variation of temporality or
// monotonicity settings between points of the same kind.
func combine(pt0, pt1 *metric_pb.Metric) bool {
	switch t0 := pt0.Data.(type) {
	case *metric_pb.Metric_IntSum:
		if t1, ok := pt1.Data.(*metric_pb.Metric_IntSum); !ok {
			return false
		} else {
			t0.IntSum.DataPoints = append(
				t0.IntSum.DataPoints,
				t1.IntSum.DataPoints[0],
			)
		}

	case *metric_pb.Metric_IntGauge:
		if t1, ok := pt1.Data.(*metric_pb.Metric_IntGauge); !ok {
			return false
		} else {
			t0.IntGauge.DataPoints = append(
				t0.IntGauge.DataPoints,
				t1.IntGauge.DataPoints[0],
			)
		}

	case *metric_pb.Metric_DoubleSum:
		if t1, ok := pt1.Data.(*metric_pb.Metric_DoubleSum); !ok {
			return false
		} else {
			t0.DoubleSum.DataPoints = append(
				t0.DoubleSum.DataPoints,
				t1.DoubleSum.DataPoints[0],
			)
		}

	case *metric_pb.Metric_DoubleGauge:
		if t1, ok := pt1.Data.(*metric_pb.Metric_DoubleGauge); !ok {
			return false
		} else {
			t0.DoubleGauge.DataPoints = append(
				t0.DoubleGauge.DataPoints,
				t1.DoubleGauge.DataPoints[0],
			)
		}

	case *metric_pb.Metric_IntHistogram:
		if t1, ok := pt1.Data.(*metric_pb.Metric_IntHistogram); !ok {
			return false
		} else {
			t0.IntHistogram.DataPoints = append(
				t0.IntHistogram.DataPoints,
				t1.IntHistogram.DataPoints[0],
			)
		}

	case *metric_pb.Metric_DoubleHistogram:
		if t1, ok := pt1.Data.(*metric_pb.Metric_DoubleHistogram); !ok {
			return false
		} else {
			t0.DoubleHistogram.DataPoints = append(
				t0.DoubleHistogram.DataPoints,
				t1.DoubleHistogram.DataPoints[0],
			)
		}

	case *metric_pb.Metric_DoubleSummary:
		if t1, ok := pt1.Data.(*metric_pb.Metric_DoubleSummary); !ok {
			return false
		} else {
			t0.DoubleSummary.DataPoints = append(
				t0.DoubleSummary.DataPoints,
				t1.DoubleSummary.DataPoints[0],
			)
		}

	default:
		return false
	}
	return true
}
