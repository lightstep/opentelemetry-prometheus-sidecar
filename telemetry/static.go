// Copyright Lightstep Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package telemetry

import (
	"bytes"
	"context"
	"fmt"
	stdlog "log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/go-logfmt/logfmt"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc/grpclog"
)

// This file adds logging configurations for:
// - "log" logger
// - "google.golang.org/grpc/grpclog" log handler
// - "go.opentelemetry.io/otel" error handler

type deferLogger struct {
	lock     sync.Mutex
	delegate log.Logger
}

var (
	uninitializedLogKVS = []interface{}{
		"logging", "uninitialized",
	}

	staticLogger = log.With(&staticDeferred)

	staticDeferred deferLogger

	verboseLevel atomic.Value
)

func SetVerboseLevel(level int) {
	verboseLevel.Store(level)
}

func VerboseLevel() int {
	return verboseLevel.Load().(int)
}

func (dl *deferLogger) Log(kvs ...interface{}) error {
	staticDeferred.lock.Lock()
	delegate := dl.delegate
	staticDeferred.lock.Unlock()

	if delegate == nil {
		kvs = append(kvs[:len(kvs):len(kvs)], uninitializedLogKVS...)
		var buf bytes.Buffer
		enc := logfmt.NewEncoder(&buf)
		_ = enc.EncodeKeyvals(kvs...)
		_, _ = fmt.Fprintln(os.Stderr, buf.String())
		return nil
	}
	return delegate.Log(kvs...)
}

func StaticLogger() log.Logger {
	return staticLogger
}

func init() {
	verboseLevel.Store(int(0))

	// Note: the NewStdlibAdapter requires one of the file options for correctness.
	stdlog.SetFlags(stdlog.Ldate | stdlog.Ltime | stdlog.Lmicroseconds | stdlog.Lshortfile)
	stdlog.SetOutput(log.NewStdlibAdapter(
		log.With(staticLogger, "component", "stdlog"),
	))

	otel.SetErrorHandler(newForOTel(log.With(staticLogger, "component", "otel")))

	grpclog.SetLoggerV2(newForGRPC(log.With(staticLogger, "component", "grpc")))
}

func StaticSetup(logger log.Logger) {
	staticDeferred.lock.Lock()
	defer staticDeferred.lock.Unlock()
	staticDeferred.delegate = logger
}

// ContextWithSIGTERM returns a context that will be cancelled on SIGTERM.
func ContextWithSIGTERM(logger log.Logger) (context.Context, context.CancelFunc) {
	ctx, cancelMain := context.WithCancel(context.Background())

	go func() {
		defer cancelMain()

		term := make(chan os.Signal)
		signal.Notify(term, os.Interrupt, syscall.SIGTERM)
		select {
		case <-term:
			level.Warn(logger).Log("msg", "received SIGTERM, exiting...")
		case <-ctx.Done():
			break
		}
	}()

	return ctx, cancelMain
}

type forOTel struct {
	logger log.Logger
}

func newForOTel(l log.Logger) forOTel {
	return forOTel{
		logger: level.Error(l),
	}
}

func (l forOTel) Handle(err error) {
	if err == nil {
		return
	}
	l.logger.Log("err", err)
}

type forGRPC struct {
	loggers [3]log.Logger
}

func newForGRPC(l log.Logger) forGRPC {
	// The gRPC logger here could be extended with configurable
	// verbosity. As this stands, turn off gRPC Info and Verbose
	// logs.
	return forGRPC{
		loggers: [3]log.Logger{
			level.Info(l),
			level.Warn(l),
			level.Error(l),
		},
	}
}

// The Info methods are disabled until some degree of verbosity is
// set; the main() function adds 1 to verbosity so that
// --log.level=debug enables gRPC logging in this way.

func (l forGRPC) Info(args ...interface{}) {
	if VerboseLevel() <= 0 {
		return
	}
	l.loggers[0].Log("msg", fmt.Sprint(args...))
}

func (l forGRPC) Infoln(args ...interface{}) {
	if VerboseLevel() <= 0 {
		return
	}
	l.loggers[0].Log("msg", fmt.Sprintln(args...))
}

func (l forGRPC) Infof(format string, args ...interface{}) {
	if VerboseLevel() <= 0 {
		return
	}
	l.loggers[0].Log("msg", fmt.Sprintf(format, args...))
}

func (l forGRPC) V(level int) bool {
	return level <= VerboseLevel()
}

func (l forGRPC) Warning(args ...interface{}) {
	l.loggers[1].Log("msg", fmt.Sprint(args...))
}

func (l forGRPC) Warningln(args ...interface{}) {
	l.loggers[1].Log("msg", fmt.Sprintln(args...))
}

func (l forGRPC) Warningf(format string, args ...interface{}) {
	l.loggers[1].Log("msg", fmt.Sprintf(format, args...))
}

func (l forGRPC) Error(args ...interface{}) {
	l.loggers[2].Log("msg", fmt.Sprint(args...))
}

func (l forGRPC) Errorln(args ...interface{}) {
	l.loggers[2].Log("msg", fmt.Sprintln(args...))
}

func (l forGRPC) Errorf(format string, args ...interface{}) {
	l.loggers[2].Log("msg", fmt.Sprintf(format, args...))
}

func (l forGRPC) Fatal(args ...interface{}) {
	l.loggers[2].Log("fatal", fmt.Sprint(args...))
	os.Exit(2)
}

func (l forGRPC) Fatalln(args ...interface{}) {
	l.loggers[2].Log("fatal", fmt.Sprintln(args...))
	os.Exit(2)
}

func (l forGRPC) Fatalf(format string, args ...interface{}) {
	l.loggers[2].Log("fatal", fmt.Sprintf(format, args...))
	os.Exit(2)
}
