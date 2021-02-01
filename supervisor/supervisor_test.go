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
	"runtime"
	"testing"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/stretchr/testify/require"
)

func TestStackRegexp(t *testing.T) {
	buffer := make([]byte, 1<<14)
	stacksz := runtime.Stack(buffer, true)
	astack := buffer[:stacksz]

	s := New(Config{
		Logger: telemetry.StaticLogger(),
	})

	require.True(t, s.isStackdump(astack), "for stack %q", string(astack))
	require.False(t, s.isStackdump([]byte(`some
other
long
text
`)))
}
