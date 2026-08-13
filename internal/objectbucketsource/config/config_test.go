package config

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestParseNotificationSettings_DefaultsWhenAbsent(t *testing.T) {
	defaults := NotificationSettings{
		Mode:                      "http",
		KafkaBrokers:              []string{"b1:9092"},
		KafkaNotificationsTopics:  []string{"t1"},
		KafkaNotificationsGroupID: "g1",
	}

	got, err := parseNotificationSettings(map[string]string{}, defaults)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, defaults) {
		t.Fatalf("expected defaults %+v, got %+v", defaults, got)
	}
}

func TestParseNotificationSettings_ConfigMapOverrides(t *testing.T) {
	defaults := NotificationSettings{Mode: "http"}
	data := map[string]string{
		"NOTIFICATIONS_MODE":           "kafka",
		"KAFKA_BROKERS":                "b1:9092, b2:9092 ",
		"KAFKA_NOTIFICATIONS_TOPICS":   "t1,t2",
		"KAFKA_NOTIFICATIONS_GROUP_ID": "grp",
	}

	got, err := parseNotificationSettings(data, defaults)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := NotificationSettings{
		Mode:                      "kafka",
		KafkaBrokers:              []string{"b1:9092", "b2:9092"},
		KafkaNotificationsTopics:  []string{"t1", "t2"},
		KafkaNotificationsGroupID: "grp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %+v, got %+v", want, got)
	}
}

func TestParseNotificationSettings_InvalidMode(t *testing.T) {
	_, err := parseNotificationSettings(map[string]string{"NOTIFICATIONS_MODE": "bogus"}, NotificationSettings{})
	if err == nil {
		t.Fatal("expected error for invalid mode, got nil")
	}
}

func TestParseNotificationSettings_KafkaRequirements(t *testing.T) {
	tests := []struct {
		name string
		data map[string]string
	}{
		{
			name: "missing brokers",
			data: map[string]string{
				"NOTIFICATIONS_MODE":           "kafka",
				"KAFKA_NOTIFICATIONS_TOPICS":   "t1",
				"KAFKA_NOTIFICATIONS_GROUP_ID": "g1",
			},
		},
		{
			name: "missing topics",
			data: map[string]string{
				"NOTIFICATIONS_MODE":           "kafka",
				"KAFKA_BROKERS":                "b1:9092",
				"KAFKA_NOTIFICATIONS_GROUP_ID": "g1",
			},
		},
		{
			name: "missing group id",
			data: map[string]string{
				"NOTIFICATIONS_MODE":         "kafka",
				"KAFKA_BROKERS":              "b1:9092",
				"KAFKA_NOTIFICATIONS_TOPICS": "t1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseNotificationSettings(tt.data, NotificationSettings{}); err == nil {
				t.Fatalf("expected error for %s, got nil", tt.name)
			}
		})
	}
}

func TestParseNotificationSettings_EmptyModeDefaultsToHTTP(t *testing.T) {
	got, err := parseNotificationSettings(map[string]string{}, NotificationSettings{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Mode != "http" {
		t.Fatalf("expected mode http, got %q", got.Mode)
	}
}

func TestParseConfig_IncludesNotifications(t *testing.T) {
	cm := &corev1.ConfigMap{
		Data: map[string]string{
			"NOTIFICATIONS_MODE":           "kafka",
			"KAFKA_BROKERS":                "b1:9092",
			"KAFKA_NOTIFICATIONS_TOPICS":   "t1",
			"KAFKA_NOTIFICATIONS_GROUP_ID": "g1",
		},
	}

	cfg, err := parseConfig(cm, NotificationSettings{Mode: "http"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Notifications.Mode != "kafka" {
		t.Fatalf("expected kafka mode, got %q", cfg.Notifications.Mode)
	}
	if cfg.NoobaaAdapter.ID != "mcg-adapter" {
		t.Fatalf("expected default noobaa adapter id, got %q", cfg.NoobaaAdapter.ID)
	}
}
