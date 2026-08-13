package notificationserver

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/IBM/sarama"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ceDispatch "github.com/functions-dev/func-operator/internal/objectbucketsource/cloudevents"
	"github.com/functions-dev/func-operator/internal/objectbucketsource/config"
)

var log = logf.Log.WithName("notification-server")

// ConfigProvider provides access to configuration
type ConfigProvider interface {
	GetConfig() config.Config
	GetKafkaConfig() *sarama.Config
	// Subscribe returns a channel that is signaled whenever the configuration is reloaded.
	Subscribe() <-chan struct{}
}

type NotificationServer struct {
	Client         client.Client
	Port           int
	ConfigProvider ConfigProvider
}

// Start runs the notification runner (HTTP server or Kafka consumer) according to
// the current configuration and supervises it: when the notification-related
// settings change in the ConfigMap, it gracefully stops the current runner and
// starts a new one with the updated settings.
func (s *NotificationServer) Start(ctx context.Context) error {
	changes := s.ConfigProvider.Subscribe()

	for {
		if ctx.Err() != nil {
			return nil
		}

		settings := s.ConfigProvider.GetConfig().Notifications
		if shutdown := s.superviseRun(ctx, changes, settings); shutdown {
			return nil
		}
	}
}

// superviseRun starts the notification runner for the given settings and blocks
// until either the parent context is cancelled (returns true, indicating
// shutdown) or a restart is required because the settings changed or the runner
// exited (returns false). It always stops the runner before returning.
func (s *NotificationServer) superviseRun(ctx context.Context, changes <-chan struct{}, settings config.NotificationSettings) (shutdown bool) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.run(runCtx, settings)
	}()

	for {
		select {
		case <-ctx.Done():
			<-errCh
			return true
		case err := <-errCh:
			// The runner exited on its own (fatal setup error). Restart it,
			// unless we're shutting down.
			if ctx.Err() != nil {
				return true
			}
			if err != nil {
				log.Error(err, "notification runner stopped unexpectedly, restarting in 5s")
				select {
				case <-ctx.Done():
					return true
				case <-time.After(5 * time.Second):
				}
			}
			return false
		case <-changes:
			newSettings := s.ConfigProvider.GetConfig().Notifications
			if notificationSettingsEqual(settings, newSettings) {
				// Unrelated configuration change (e.g. adapter IDs); keep running.
				continue
			}
			log.Info("notification settings changed, restarting notification runner",
				"old-mode", settings.Mode, "new-mode", newSettings.Mode,
				"old-brokers", settings.KafkaBrokers, "new-brokers", newSettings.KafkaBrokers,
				"old-topics", settings.KafkaNotificationsTopics, "new-topics", newSettings.KafkaNotificationsTopics,
				"old-group-id", settings.KafkaNotificationsGroupID, "new-group-id", newSettings.KafkaNotificationsGroupID)
			cancel()
			<-errCh
			return false
		}
	}
}

// run starts the notification transport for the given settings and blocks until
// ctx is cancelled or a fatal error occurs.
func (s *NotificationServer) run(ctx context.Context, settings config.NotificationSettings) error {
	var kafkaProducer sarama.SyncProducer
	if len(settings.KafkaBrokers) > 0 {
		var err error
		kafkaCfg := s.ConfigProvider.GetKafkaConfig()
		kafkaProducer, err = ceDispatch.NewKafkaProducer(settings.KafkaBrokers, kafkaCfg)
		if err != nil {
			return fmt.Errorf("creating kafka producer: %w", err)
		}
		defer func() { _ = kafkaProducer.Close() }()
		log.Info("kafka producer initialized", "brokers", settings.KafkaBrokers)
	}

	handler := &notificationHandler{client: s.Client, kafkaProducer: kafkaProducer}

	if settings.Mode == "kafka" {
		return s.startKafkaConsumer(ctx, handler, settings)
	}
	return s.startHTTPServer(ctx, handler)
}

func (s *NotificationServer) startHTTPServer(ctx context.Context, handler *notificationHandler) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handler.handleNotification)

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Error(err, "notification server shutdown error")
		}
	}()

	log.Info("starting notification HTTP server", "port", s.Port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("notification server: %w", err)
	}
	return nil
}

func (s *NotificationServer) startKafkaConsumer(ctx context.Context, handler *notificationHandler, settings config.NotificationSettings) error {
	kafkaCfg := s.ConfigProvider.GetKafkaConfig()
	consumerConfig := *kafkaCfg
	consumerConfig.Consumer.Return.Errors = true
	consumerConfig.Consumer.Offsets.Initial = sarama.OffsetNewest

	consumerGroup, err := sarama.NewConsumerGroup(settings.KafkaBrokers, settings.KafkaNotificationsGroupID, &consumerConfig)
	if err != nil {
		return fmt.Errorf("creating kafka consumer group: %w", err)
	}
	defer func() { _ = consumerGroup.Close() }()

	log.Info("starting kafka notification consumer",
		"topics", settings.KafkaNotificationsTopics,
		"group", settings.KafkaNotificationsGroupID,
		"brokers", settings.KafkaBrokers)

	go func() {
		for err := range consumerGroup.Errors() {
			log.Error(err, "kafka consumer group error")
		}
	}()

	cgHandler := &consumerGroupHandler{handler: handler}

	for {
		if err := consumerGroup.Consume(ctx, settings.KafkaNotificationsTopics, cgHandler); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Error(err, "kafka consumer group session error, restarting")
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

func (s *NotificationServer) NeedLeaderElection() bool {
	return false
}

// notificationSettingsEqual reports whether the two notification settings are
// equivalent for the purposes of deciding whether the runner must be restarted.
func notificationSettingsEqual(a, b config.NotificationSettings) bool {
	return a.Mode == b.Mode &&
		a.KafkaNotificationsGroupID == b.KafkaNotificationsGroupID &&
		stringSlicesEqual(a.KafkaBrokers, b.KafkaBrokers) &&
		stringSlicesEqual(a.KafkaNotificationsTopics, b.KafkaNotificationsTopics)
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
