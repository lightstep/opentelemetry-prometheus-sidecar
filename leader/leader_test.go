package leader

import (
	"context"
	"sync"
	"testing"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
)

type testController struct {
	lock      sync.Mutex
	cond      *sync.Cond
	started   bool
	newLeader bool
}

func newTest() *testController {
	tc := &testController{}
	tc.cond = sync.NewCond(&tc.lock)
	return tc
}

func TestLeaderElection(t *testing.T) {
	fc := fake.NewSimpleClientset()
	tc := newTest()
	le, err := NewCandidate(fc, "default", "hello", "world", tc, telemetry.DefaultLogger())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, le.Start(ctx))

	tc.lock.Lock()
	for !tc.started || !tc.newLeader {
		tc.cond.Wait()
	}

	tc.lock.Unlock()
}

func (c *testController) OnStartedLeading(ctx context.Context) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.started = true
	c.cond.Broadcast()
}

func (c *testController) OnStoppedLeading() {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.started = false
	c.cond.Broadcast()
}

func (c *testController) OnNewLeader(self bool, identity string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.newLeader = self
	c.cond.Broadcast()
}
