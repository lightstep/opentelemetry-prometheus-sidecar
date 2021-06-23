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

type Controller interface {
	OnNewLeader(self bool, identity string)
	OnStartedLeading(ctx context.Context)
	OnStoppedLeading()
}

type candidate struct {
	client  kubernetes.Interface
	ctrl    Controller
	id      string
	elector *leaderelection.LeaderElector
	logger  log.Logger
}

type LoggingController struct {
	log.Logger
}

func NewClient() (*kubernetes.Clientset, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, errors.Wrap(err, "in-cluster k8s config")
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "new k8s client")
	}
	return client, err
}

func NewCandidate(client kubernetes.Interface, namespace, name, id string, ctrl Controller, logger log.Logger) (Candidate, error) {
	c := &candidate{
		client: client,
		ctrl:   ctrl,
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
			OnStartedLeading: ctrl.OnStartedLeading,
			OnStoppedLeading: ctrl.OnStoppedLeading,
			OnNewLeader: func(id string) {
				ctrl.OnNewLeader(lock.LockConfig.Identity == c.id, id)
			},
		},
	}

	elector, err := leaderelection.NewLeaderElector(lec)
	if err != nil {
		return nil, errors.Wrap(err, "start elector")
	}
	c.elector = elector
	return c, nil

}

func (c *candidate) Start(ctx context.Context) error {
	// This runs until the context is canceled by main().
	go c.elector.Run(ctx)

	return nil
}

func (c LoggingController) OnStartedLeading(ctx context.Context) {
	level.Info(c.Logger).Log("msg", "this sidecar started leading")
}

func (c LoggingController) OnStoppedLeading() {
	level.Info(c.Logger).Log("msg", "this sidecar stopped leading")
}

func (c LoggingController) OnNewLeader(self bool, identity string) {
	if self {
		level.Info(c.Logger).Log("msg", "this sidecar has become leader", "id", identity)
		return
	}
	level.Info(c.Logger).Log("msg", "another sidecar became leader is", "id", identity)
}
