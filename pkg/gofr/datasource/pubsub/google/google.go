// Package google provides a client for interacting with Google Cloud Pub/Sub.This package facilitates interaction with
// Google Cloud Pub/Sub, allowing publishing and subscribing to topics, managing subscriptions, and handling messages.
package google

import (
	"context"
	"errors"
	"strings"
	"time"

	gcPubSub "cloud.google.com/go/pubsub"

	"gofr.dev/pkg/gofr/datasource/pubsub"
)

const (
	gcpBackend    = "GCP"
	modePublish   = "PUB"
	modeSubscribe = "SUB"
)

var (
	errProjectIDNotProvided    = errors.New("google project id not provided")
	errSubscriptionNotProvided = errors.New("subscription name not provided")
)

type Config struct {
	ProjectID        string
	SubscriptionName string
}

type googleClient struct {
	Config

	client  Client
	logger  pubsub.Logger
	metrics Metrics
}

//nolint:revive // We do not want anyone using the client without initialization steps.
func New(conf Config, logger pubsub.Logger, metrics Metrics) *googleClient {
	err := validateConfigs(&conf)
	if err != nil {
		logger.Errorf("google pubsub could not be configured, err: %v", err)

		return nil
	}

	client, err := gcPubSub.NewClient(context.Background(), conf.ProjectID)
	if err != nil {
		return &googleClient{
			Config: conf,
		}
	}

	logger.Logf("connected to google pubsub client, projectID: %s", client.Project())

	return &googleClient{
		Config:  conf,
		client:  client,
		logger:  logger,
		metrics: metrics,
	}
}

func validateConfigs(conf *Config) error {
	if conf.ProjectID == "" {
		return errProjectIDNotProvided
	}

	if conf.SubscriptionName == "" {
		return errSubscriptionNotProvided
	}

	return nil
}

func (g *googleClient) Publish(ctx context.Context, topic string, message []byte) error {
	g.metrics.IncrementCounter(ctx, "app_pubsub_publish_total_count", "topic", topic)

	t, err := g.getTopic(ctx, topic)
	if err != nil {
		g.logger.Errorf("error creating %s err: %v", topic, err)

		return err
	}

	result := t.Publish(ctx, &gcPubSub.Message{
		Data:        message,
		PublishTime: time.Now(),
	})

	id, err := result.Get(ctx)
	if err != nil {
		g.logger.Errorf("error publishing to google topic %s err: %v", topic, err)

		return err
	}

	g.logger.Debug(&pubsub.Log{
		Mode:          modePublish,
		MessageID:     id,
		MessageValue:  string(message),
		Topic:         topic,
		Host:          g.ProjectID,
		PubSubBackend: gcpBackend,
	})

	g.metrics.IncrementCounter(ctx, "app_pubsub_publish_success_count", "topic", topic)

	return nil
}

func (g *googleClient) Subscribe(ctx context.Context, topic string) (*pubsub.Message, error) {
	g.metrics.IncrementCounter(ctx, "app_pubsub_subscribe_total_count", "topic", topic)

	var m = pubsub.NewMessage(ctx)

	t, err := g.getTopic(ctx, topic)
	if err != nil {
		return nil, err
	}

	subscription, err := g.getSubscription(ctx, t)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)

	err = subscription.Receive(ctx, func(_ context.Context, msg *gcPubSub.Message) {
		defer cancel()

		m.Topic = topic
		m.Value = msg.Data
		m.MetaData = msg.Attributes
		m.Committer = newGoogleMessage(msg)

		g.logger.Debug(&pubsub.Log{
			Mode:          modeSubscribe,
			MessageID:     msg.ID,
			MessageValue:  string(m.Value),
			Topic:         topic,
			Host:          g.Config.ProjectID,
			PubSubBackend: gcpBackend,
		})
	})

	if err != nil {
		g.logger.Errorf("error getting a message from google: %s", err.Error())

		return nil, err
	}

	g.metrics.IncrementCounter(ctx, "app_pubsub_subscribe_success_count", "topic", topic)

	return m, nil
}

func (g *googleClient) getTopic(ctx context.Context, topic string) (*gcPubSub.Topic, error) {
	t := g.client.Topic(topic)

	// check if topic exists, if not create the topic
	if ok, err := t.Exists(ctx); !ok || err != nil {
		_, errTopicCreate := g.client.CreateTopic(ctx, topic)
		if errTopicCreate != nil {
			return nil, errTopicCreate
		}
	}

	return t, nil
}

func (g *googleClient) getSubscription(ctx context.Context, topic *gcPubSub.Topic) (*gcPubSub.Subscription, error) {
	subscription := g.client.Subscription(g.SubscriptionName + "-" + topic.ID())

	// check if subscription already exists or not
	ok, err := subscription.Exists(context.Background())
	if err != nil {
		g.logger.Errorf("unable to check the existence of subscription, err: %v ", err.Error())

		return nil, err
	}

	// if subscription is not present, create a new
	if !ok {
		subscription, err = g.client.CreateSubscription(ctx, g.SubscriptionName+"-"+topic.ID(), gcPubSub.SubscriptionConfig{
			Topic: topic,
		})

		if err != nil {
			return nil, err
		}
	}

	return subscription, nil
}

func (g *googleClient) DeleteTopic(ctx context.Context, name string) error {
	err := g.client.Topic(name).Delete(ctx)

	if err != nil && strings.Contains(err.Error(), "Topic not found") {
		return nil
	}

	return err
}

func (g *googleClient) CreateTopic(ctx context.Context, name string) error {
	_, err := g.client.CreateTopic(ctx, name)

	if err != nil && strings.Contains(err.Error(), "Topic already exists") {
		return nil
	}

	return err
}
