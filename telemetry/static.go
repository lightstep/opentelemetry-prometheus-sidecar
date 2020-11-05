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
	"fmt"
	stdlog "log"
	"os"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"go.opentelemetry.io/otel/api/global"
	"google.golang.org/grpc/grpclog"
)

// staticSetup adds logging configurations for:
// - "log" logger
// - "google.golang.org/grpc/grpclog" log handler
// - "go.opentelemetry.io/otel/api/global" error handler
func staticSetup(logger log.Logger) {
	stdlog.SetOutput(log.NewStdlibAdapter(log.With(logger, "component", "stdlog")))

	global.SetErrorHandler(newForOTel(logger))

	grpclog.SetLoggerV2(newForGRPC(logger))
}

type forOTel struct {
	logger log.Logger
}

func newForOTel(l log.Logger) forOTel {
	return forOTel{
		logger: level.Error(log.With(l, "component", "otel")),
	}
}

func (l forOTel) Handle(err error) {
	l.logger.Log("error", err)
}

type forGRPC struct {
	loggers [4]log.Logger
}

func newForGRPC(l log.Logger) forGRPC {
	l = log.With(l, "component", "grpc")
	// The gRPC logger here could be extended with configurable
	// verbosity. As this stands, turn off gRPC Info and Verbose
	// logs.
	return forGRPC{
		loggers: [4]log.Logger{
			nil, // level.Debug(l),
			nil, // level.Info(l),
			level.Warn(l),
			level.Error(l),
		},
	}
}

// Info and Verbose logs are no-ops.

func (l forGRPC) Info(args ...interface{})                 {}
func (l forGRPC) Infoln(args ...interface{})               {}
func (l forGRPC) Infof(format string, args ...interface{}) {}
func (l forGRPC) V(_ int) bool                             { return false }

func (l forGRPC) Warning(args ...interface{}) {
	l.loggers[2].Log("message", fmt.Sprint(args...))
}

func (l forGRPC) Warningln(args ...interface{}) {
	l.loggers[2].Log("message", fmt.Sprintln(args...))
}

func (l forGRPC) Warningf(format string, args ...interface{}) {
	l.loggers[2].Log("message", fmt.Sprintf(format, args...))
}

func (l forGRPC) Error(args ...interface{}) {
	l.loggers[3].Log("message", fmt.Sprint(args...))
}

func (l forGRPC) Errorln(args ...interface{}) {
	l.loggers[3].Log("message", fmt.Sprintln(args...))
}

func (l forGRPC) Errorf(format string, args ...interface{}) {
	l.loggers[3].Log("message", fmt.Sprintf(format, args...))
}

func (l forGRPC) Fatal(args ...interface{}) {
	l.loggers[3].Log("fatal", fmt.Sprint(args...))
	os.Exit(2)
}

func (l forGRPC) Fatalln(args ...interface{}) {
	l.loggers[3].Log("fatal", fmt.Sprintln(args...))
	os.Exit(2)
}

func (l forGRPC) Fatalf(format string, args ...interface{}) {
	l.loggers[3].Log("fatal", fmt.Sprintf(format, args...))
	os.Exit(2)
}
