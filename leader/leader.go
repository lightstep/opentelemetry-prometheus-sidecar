package leader

import (
	"context"
	"fmt"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

type Candidate interface {
	Start(ctx context.Context) error
}

type candidate struct {
	client  *kubernetes.Clientset
	id      string
	elector *leaderelection.LeaderElector
	logger  log.Logger
}

func NewCandidate(namespace, name, id string, logger log.Logger) (Candidate, error) {

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, errors.Wrap(err, "in-cluster k8s config")
	}

	c := &candidate{
		client: kubernetes.NewForConfigOrDie(cfg),
		id:     id,
		logger: logger,
	}

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Client: c.client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: id,
		},
	}

	lec := leaderelection.LeaderElectionConfig{
		Lock:            lock,
		Name:            fmt.Sprint(namespace, "-", name, ":", id),
		ReleaseOnCancel: true,
		LeaseDuration:   60 * time.Second,
		RenewDeadline:   15 * time.Second,
		RetryPeriod:     5 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: c.onStartedLeading,
			OnStoppedLeading: c.onStoppedLeading,
			OnNewLeader:      c.onNewLeader,
		},
	}

	c.elector, err = leaderelection.NewLeaderElector(lec)
	if err != nil {
		return nil, errors.Wrap(err, "start elector")
	}

	return c, nil

}
func (c *candidate) Start(ctx context.Context) error {
	// ...
	go c.elector.Run(ctx)
	// ...
	return nil
}

func (c *candidate) onStartedLeading(ctx context.Context) {
	level.Info(c.logger).Log("msg", "started leading")
}

func (c *candidate) onStoppedLeading() {
	level.Info(c.logger).Log("msg", "stopped leading")
}

func (c *candidate) onNewLeader(identity string) {
	if identity == c.id {
		level.Info(c.logger).Log("msg", "new leader is ME", "id", identity)
		return
	}
	level.Info(c.logger).Log("msg", "new leader is SOMEONE", "id", identity)
}
