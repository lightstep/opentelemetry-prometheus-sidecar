// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package supervisor

import (
	"os"
	"os/exec"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
)

func Start(args []string, logger log.Logger) bool {
	if err := start(args, logger); err != nil {
		level.Error(logger).Log("msg", "sidecar failed", "err", err)
		return false
	}
	return true
}

func start(args []string, logger log.Logger) error {
	cmd := exec.Command(args[0], args[1:]...)

	// @@@ TODO here
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

	if err := cmd.Start(); err != nil {
		return errors.Wrap(err, "supervisor exec")
	}

	err := cmd.Wait()

	if err == nil {
		level.Info(logger).Log("msg", "supervisor shutdown")
		return nil
	}

	// TODO log it, etc

	return err
}
