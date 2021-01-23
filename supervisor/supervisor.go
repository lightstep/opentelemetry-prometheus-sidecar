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
	"os/exec"

	"github.com/pkg/errors"
)

func Start(args []string) {
	cmd := exec.Command(args[0], args[1:])

	if err := cmd.Start(); err != nil {
		return errors.Wrap(err, "supervisor exec")
	}

	if err := cmd.Wait(); err != nil {
		return errors.Wrap(err, "supervisor wait")
	}

	return nil
}
