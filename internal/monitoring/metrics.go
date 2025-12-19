package monitoring

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	DeployDurationMetricName   = "deploy_duration_seconds"
	GitCloneDurationMetricName = "git_clone_duration_seconds"
)

var (
	DeployDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: DeployDurationMetricName,
		Help: "Time spent running the deploy command",
	})

	GitCloneDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: GitCloneDurationMetricName,
		Help: "Time spent running the git clone command",
	})
)

func RegisterMetrics() {
	metrics.Registry.MustRegister(
		DeployDuration,
		GitCloneDuration,
	)
}
