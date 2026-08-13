package config

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/IBM/sarama"
	kafkaconfig "github.com/functions-dev/func-operator/internal/objectbucketsource/kafka"
)

var log = logf.Log.WithName("adapter-config")

// AdapterBackendConfig holds configuration for a single storage backend adapter
type AdapterBackendConfig struct {
	ID                  string
	TopicARN            string
	StorageClassPattern *regexp.Regexp
}

// NotificationSettings holds the transport configuration that controls how the
// adapter receives NooBaa/RadosGW notifications. These settings can be changed at
// runtime via the ConfigMap; the notification server restarts its Kafka consumer
// when any of them change.
type NotificationSettings struct {
	// Mode is "http" or "kafka".
	Mode                      string
	KafkaBrokers              []string
	KafkaNotificationsTopics  []string
	KafkaNotificationsGroupID string
}

// Config holds runtime-configurable settings for the objectbucket-notifications-adapter
type Config struct {
	NoobaaAdapter  AdapterBackendConfig
	RadosgwAdapter AdapterBackendConfig
	Notifications  NotificationSettings
}

// Provider provides access to the current configuration and watches for changes
type Provider struct {
	mu     sync.RWMutex
	config Config

	namespace     string
	configMapName string
	clientset     *kubernetes.Clientset
	cancelWatch   context.CancelFunc

	// defaults holds the notification settings supplied via command-line flags.
	// They are used whenever the corresponding ConfigMap keys are absent.
	defaults NotificationSettings

	kafkaConfigMu sync.RWMutex
	kafkaConfig   *sarama.Config
	kafkaSecret   string

	subscribersMu sync.Mutex
	subscribers   []chan struct{}
}

// NewProvider creates a new configuration provider that watches a ConfigMap.
// The defaults are used for any notification settings not present in the ConfigMap.
func NewProvider(ctx context.Context, namespace, configMapName string, defaults NotificationSettings) (*Provider, error) {
	clientset, err := kubernetes.NewForConfig(ctrl.GetConfigOrDie())
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes clientset: %w", err)
	}

	p := &Provider{
		namespace:     namespace,
		configMapName: configMapName,
		clientset:     clientset,
		defaults:      defaults,
	}

	if err := p.loadConfig(ctx); err != nil {
		return nil, fmt.Errorf("loading initial config: %w", err)
	}

	watchCtx, cancel := context.WithCancel(context.Background())
	p.cancelWatch = cancel
	go p.watchConfigMap(watchCtx)

	return p, nil
}

// GetConfig returns a copy of the current configuration
func (p *Provider) GetConfig() Config {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config
}

// GetKafkaConfig returns the current Kafka configuration
func (p *Provider) GetKafkaConfig() *sarama.Config {
	p.kafkaConfigMu.RLock()
	defer p.kafkaConfigMu.RUnlock()
	return p.kafkaConfig
}

// Subscribe returns a channel that receives a signal whenever the configuration
// is successfully reloaded. The channel is buffered (size 1) and signals are
// coalesced, so a slow subscriber never blocks the config watcher.
func (p *Provider) Subscribe() <-chan struct{} {
	ch := make(chan struct{}, 1)
	p.subscribersMu.Lock()
	p.subscribers = append(p.subscribers, ch)
	p.subscribersMu.Unlock()
	return ch
}

func (p *Provider) notifySubscribers() {
	p.subscribersMu.Lock()
	defer p.subscribersMu.Unlock()
	for _, ch := range p.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Stop stops watching the ConfigMap
func (p *Provider) Stop() {
	if p.cancelWatch != nil {
		p.cancelWatch()
	}
}

// NeedLeaderElection implements the manager.Runnable interface
func (p *Provider) NeedLeaderElection() bool {
	return false
}

// Start implements the manager.Runnable interface
func (p *Provider) Start(ctx context.Context) error {
	<-ctx.Done()
	p.Stop()
	return nil
}

func (p *Provider) loadConfig(ctx context.Context) error {
	cm, err := p.clientset.CoreV1().ConfigMaps(p.namespace).Get(ctx, p.configMapName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting ConfigMap %s/%s: %w", p.namespace, p.configMapName, err)
	}

	config, err := parseConfig(cm, p.defaults)
	if err != nil {
		return fmt.Errorf("parsing ConfigMap: %w", err)
	}

	kafkaSecret := cm.Data["KAFKA_SECRET"]
	var kafkaCfg *sarama.Config
	if kafkaSecret != "" {
		secret, err := p.clientset.CoreV1().Secrets(p.namespace).Get(ctx, kafkaSecret, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("reading KAFKA_SECRET %s/%s: %w", p.namespace, kafkaSecret, err)
		}
		kafkaCfg, err = kafkaconfig.NewConfig(secret.Data)
		if err != nil {
			return fmt.Errorf("configuring kafka from secret %s: %w", kafkaSecret, err)
		}
		log.Info("kafka configured from secret", "name", kafkaSecret, "namespace", p.namespace)
	} else {
		kafkaCfg, err = kafkaconfig.NewConfig(nil)
		if err != nil {
			return fmt.Errorf("creating default kafka config: %w", err)
		}
	}

	p.mu.Lock()
	p.config = config
	p.mu.Unlock()

	p.kafkaConfigMu.Lock()
	p.kafkaConfig = kafkaCfg
	p.kafkaSecret = kafkaSecret
	p.kafkaConfigMu.Unlock()

	log.Info("configuration loaded",
		"noobaa-adapter-id", config.NoobaaAdapter.ID,
		"radosgw-adapter-id", config.RadosgwAdapter.ID,
		"notifications-mode", config.Notifications.Mode,
		"kafka-brokers", config.Notifications.KafkaBrokers,
		"kafka-notifications-topics", config.Notifications.KafkaNotificationsTopics,
		"kafka-notifications-group-id", config.Notifications.KafkaNotificationsGroupID)

	p.notifySubscribers()

	return nil
}

func (p *Provider) watchConfigMap(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		watcher, err := p.clientset.CoreV1().ConfigMaps(p.namespace).Watch(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("metadata.name=%s", p.configMapName),
		})
		if err != nil {
			log.Error(err, "failed to create ConfigMap watcher, retrying in 5s")
			select {
			case <-ctx.Done():
				return
			case <-ctrl.SetupSignalHandler().Done():
				return
			}
			continue
		}

		log.Info("watching ConfigMap for changes", "name", p.configMapName, "namespace", p.namespace)

		func() {
			defer watcher.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case event, ok := <-watcher.ResultChan():
					if !ok {
						log.Info("ConfigMap watch channel closed, restarting watcher")
						return
					}

					if event.Type == watch.Modified || event.Type == watch.Added {
						cm, ok := event.Object.(*corev1.ConfigMap)
						if !ok {
							log.Error(fmt.Errorf("unexpected object type"), "failed to cast to ConfigMap")
							continue
						}

						log.Info("ConfigMap changed, reloading configuration", "name", cm.Name)
						if err := p.loadConfig(ctx); err != nil {
							log.Error(err, "failed to reload configuration")
						} else {
							log.Info("configuration reloaded successfully")
						}
					} else if event.Type == watch.Deleted {
						log.Error(fmt.Errorf("ConfigMap deleted"), "adapter configuration unavailable", "name", p.configMapName)
					}
				}
			}
		}()
	}
}

func parseConfig(cm *corev1.ConfigMap, defaults NotificationSettings) (Config, error) {
	noobaaPattern := getOrDefault(cm.Data, "NOOBAA_ADAPTER_STORAGECLASS_PATTERN", `.*noobaa\.io$`)
	radosgwPattern := getOrDefault(cm.Data, "RADOSGW_ADAPTER_STORAGECLASS_PATTERN", `.*ceph-rgw$`)

	noobaaRe, err := regexp.Compile(noobaaPattern)
	if err != nil {
		return Config{}, fmt.Errorf("invalid NOOBAA_ADAPTER_STORAGECLASS_PATTERN: %w", err)
	}

	radosgwRe, err := regexp.Compile(radosgwPattern)
	if err != nil {
		return Config{}, fmt.Errorf("invalid RADOSGW_ADAPTER_STORAGECLASS_PATTERN: %w", err)
	}

	notifications, err := parseNotificationSettings(cm.Data, defaults)
	if err != nil {
		return Config{}, err
	}

	config := Config{
		NoobaaAdapter: AdapterBackendConfig{
			ID:                  getOrDefault(cm.Data, "NOOBAA_ADAPTER_ID", "mcg-adapter"),
			TopicARN:            getOrDefault(cm.Data, "NOOBAA_ADAPTER_TOPIC_ARN", "mcg-adapter-connection/connect.json"),
			StorageClassPattern: noobaaRe,
		},
		RadosgwAdapter: AdapterBackendConfig{
			ID:                  getOrDefault(cm.Data, "RADOSGW_ADAPTER_ID", "rgw-adapter"),
			TopicARN:            getOrDefault(cm.Data, "RADOSGW_ADAPTER_TOPIC_ARN", "arn:aws:sns:ocs-storagecluster-cephobjectstore::rgw-adapter-notifications"),
			StorageClassPattern: radosgwRe,
		},
		Notifications: notifications,
	}

	return config, nil
}

// parseNotificationSettings resolves the notification transport settings from the
// ConfigMap, falling back to the provided defaults (from command-line flags) when
// a key is absent. It validates the resulting settings so that invalid ConfigMap
// changes are rejected and the previous valid configuration is retained.
func parseNotificationSettings(data map[string]string, defaults NotificationSettings) (NotificationSettings, error) {
	mode := getOrDefault(data, "NOTIFICATIONS_MODE", defaults.Mode)
	if mode == "" {
		mode = "http"
	}
	if mode != "http" && mode != "kafka" {
		return NotificationSettings{}, fmt.Errorf("invalid NOTIFICATIONS_MODE %q: must be \"http\" or \"kafka\"", mode)
	}

	settings := NotificationSettings{
		Mode:                      mode,
		KafkaBrokers:              defaults.KafkaBrokers,
		KafkaNotificationsTopics:  defaults.KafkaNotificationsTopics,
		KafkaNotificationsGroupID: getOrDefault(data, "KAFKA_NOTIFICATIONS_GROUP_ID", defaults.KafkaNotificationsGroupID),
	}
	if v, ok := data["KAFKA_BROKERS"]; ok && strings.TrimSpace(v) != "" {
		settings.KafkaBrokers = splitAndTrim(v)
	}
	if v, ok := data["KAFKA_NOTIFICATIONS_TOPICS"]; ok && strings.TrimSpace(v) != "" {
		settings.KafkaNotificationsTopics = splitAndTrim(v)
	}

	if mode == "kafka" {
		if len(settings.KafkaBrokers) == 0 {
			return NotificationSettings{}, fmt.Errorf("KAFKA_BROKERS is required when NOTIFICATIONS_MODE=kafka")
		}
		if len(settings.KafkaNotificationsTopics) == 0 {
			return NotificationSettings{}, fmt.Errorf("KAFKA_NOTIFICATIONS_TOPICS is required when NOTIFICATIONS_MODE=kafka")
		}
		if settings.KafkaNotificationsGroupID == "" {
			return NotificationSettings{}, fmt.Errorf("KAFKA_NOTIFICATIONS_GROUP_ID is required when NOTIFICATIONS_MODE=kafka")
		}
	}

	return settings, nil
}

func getOrDefault(data map[string]string, key, defaultValue string) string {
	if v, ok := data[key]; ok && v != "" {
		return v
	}
	return defaultValue
}

// splitAndTrim splits a comma-separated string, trimming whitespace and dropping
// empty entries.
func splitAndTrim(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
