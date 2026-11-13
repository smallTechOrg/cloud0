package monclient

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"github.com/smallTechOrg/cloud0/internal/alerting"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// SlackHint is shown for channel types that need console OAuth.
const SlackHint = "Slack channels need console OAuth: create it in Cloud Console → Monitoring → Notification channels, then pick it here."

// slackCreateMsg is the ErrInvalidParams message for Create of an OAuth type.
const slackCreateMsg = "authorize Slack in the Google Cloud console, then pick the channel here"

const unverifiedEmailWarning = "Email channel is not verified: notifications may not be delivered. Open the channel in the console and verify the address."

// creatableTypes can be created through the API without console OAuth, with
// the label keys each requires (Labels or SensitiveLabels).
var creatableTypes = map[string][]string{
	"email":             {"email_address"},
	"sms":               {"number"},
	"webhook_tokenauth": {"url"},
	"webhook_basicauth": {"url", "username", "password"},
	"pagerduty":         {"service_key"},
	"pubsub":            {"topic"},
}

// oauthTypes need the console to authorize.
var oauthTypes = map[string]bool{"slack": true, "google_chat": true}

// Channels implements alerting.ChannelClient.
type Channels struct {
	read  *monitoring.NotificationChannelClient
	write *monitoring.NotificationChannelClient // nil = not configured
}

var _ alerting.ChannelClient = (*Channels)(nil)

// NewChannels builds the reader client and, when creds.Writer is set, the writer.
func NewChannels(ctx context.Context, creds Creds) (*Channels, error) {
	r, err := monitoring.NewNotificationChannelClient(ctx, creds.Reader...)
	if err != nil {
		return nil, fmt.Errorf("monclient: notification channel reader client: %w", err)
	}
	c := &Channels{read: r}
	if creds.Writer != nil {
		w, err := monitoring.NewNotificationChannelClient(ctx, creds.Writer...)
		if err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("monclient: notification channel writer client: %w", err)
		}
		c.write = w
	}
	return c, nil
}

// Close releases both clients.
func (c *Channels) Close() error {
	var first error
	if c.read != nil {
		first = c.read.Close()
	}
	if c.write != nil {
		if err := c.write.Close(); first == nil {
			first = err
		}
	}
	return first
}

// List returns the project's channels ordered by display name; secrets are never returned.
func (c *Channels) List(ctx context.Context, project string) ([]alerting.Channel, error) {
	if strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("%w: project is required", alerting.ErrInvalidParams)
	}
	it := c.read.ListNotificationChannels(ctx, &monitoringpb.ListNotificationChannelsRequest{Name: "projects/" + project})
	out := []alerting.Channel{}
	for {
		ch, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, mapErr(err, false, roleChannelEditor, project)
		}
		out = append(out, channelToView(ch))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].DisplayName != out[j].DisplayName {
			return out[i].DisplayName < out[j].DisplayName
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Descriptors lists the channel types of the project and flags creatable ones.
func (c *Channels) Descriptors(ctx context.Context, project string) ([]alerting.ChannelDescriptor, error) {
	if strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("%w: project is required", alerting.ErrInvalidParams)
	}
	it := c.read.ListNotificationChannelDescriptors(ctx, &monitoringpb.ListNotificationChannelDescriptorsRequest{Name: "projects/" + project})
	out := []alerting.ChannelDescriptor{}
	for {
		d, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, mapErr(err, false, roleChannelEditor, project)
		}
		out = append(out, descriptorToView(d))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

// Create makes a notification channel from the draft. Sensitive labels are
// merged into the GCP labels map and never logged or returned.
func (c *Channels) Create(ctx context.Context, d alerting.ChannelDraft) (alerting.Channel, error) {
	if c.write == nil {
		return alerting.Channel{}, alerting.ErrNotConfigured
	}
	pb, err := channelDraftToPB(d)
	if err != nil {
		return alerting.Channel{}, err
	}
	got, err := c.write.CreateNotificationChannel(ctx, &monitoringpb.CreateNotificationChannelRequest{
		Name:                "projects/" + d.Project,
		NotificationChannel: pb,
	})
	if err != nil {
		return alerting.Channel{}, mapErr(err, true, roleChannelEditor, d.Project)
	}
	slog.Info("alerting: notification channel created", "type", d.Type, "display_name", d.DisplayName)
	return channelToView(got), nil
}

// Delete removes a channel (not forced: GCP refuses while policies reference it).
func (c *Channels) Delete(ctx context.Context, name string) error {
	if c.write == nil {
		return alerting.ErrNotConfigured
	}
	if !validName(name, "notificationChannels") {
		return fmt.Errorf("%w: channel name must look like %s", alerting.ErrInvalidParams, channelNamePattern)
	}
	err := c.write.DeleteNotificationChannel(ctx, &monitoringpb.DeleteNotificationChannelRequest{Name: name})
	if err != nil {
		return mapErr(err, true, roleChannelEditor, projectOf(name))
	}
	slog.Info("alerting: notification channel deleted", "name", name)
	return nil
}

// ---- conversion (pure) ------------------------------------------------------

func channelToView(ch *monitoringpb.NotificationChannel) alerting.Channel {
	v := alerting.Channel{
		Name:               ch.GetName(),
		Project:            projectOf(ch.GetName()),
		Type:               ch.GetType(),
		DisplayName:        ch.GetDisplayName(),
		Enabled:            ch.GetEnabled() == nil || ch.GetEnabled().GetValue(),
		VerificationStatus: ch.GetVerificationStatus().String(),
		Warnings:           []string{},
	}
	if ch.GetType() == "email" {
		v.Address = MaskEmail(ch.GetLabels()["email_address"])
		if v.VerificationStatus != "VERIFIED" {
			v.Warnings = append(v.Warnings, unverifiedEmailWarning)
		}
	}
	if !v.Enabled {
		v.Warnings = append(v.Warnings, "Channel is disabled: it will not receive notifications.")
	}
	return v
}

// MaskEmail turns "alerts@example.com" into "al***@example.com". Anything that
// is not an address yields "***".
func MaskEmail(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at <= 0 {
		return "***"
	}
	local, domain := addr[:at], addr[at:]
	keep := 2
	if len(local) <= 2 {
		keep = 1
	}
	return local[:keep] + "***" + domain
}

var sensitiveKeys = map[string]bool{"auth_token": true, "password": true, "service_key": true, "token": true}

func descriptorToView(d *monitoringpb.NotificationChannelDescriptor) alerting.ChannelDescriptor {
	t := d.GetType()
	_, creatable := creatableTypes[t]
	v := alerting.ChannelDescriptor{
		Type:        t,
		DisplayName: d.GetDisplayName(),
		Creatable:   creatable && !oauthTypes[t],
		Labels:      []alerting.LabelSpec{},
	}
	switch {
	case oauthTypes[t]:
		v.Hint = SlackHint
		if t != "slack" {
			v.Hint = strings.ReplaceAll(SlackHint, "Slack", d.GetDisplayName())
		}
	case !creatable:
		v.Hint = "This channel type cannot be created through the API: create it in Cloud Console → Monitoring → Notification channels, then pick it here."
	}
	if v.Creatable {
		for _, l := range d.GetLabels() {
			v.Labels = append(v.Labels, alerting.LabelSpec{
				Key:         l.GetKey(),
				Description: l.GetDescription(),
				Required:    true,
				Sensitive:   sensitiveKeys[l.GetKey()] || (strings.HasPrefix(t, "webhook") && l.GetKey() == "url"),
			})
		}
	}
	return v
}

// channelDraftToPB validates the draft and builds the request body.
func channelDraftToPB(d alerting.ChannelDraft) (*monitoringpb.NotificationChannel, error) {
	if strings.TrimSpace(d.Project) == "" || strings.TrimSpace(d.Type) == "" || strings.TrimSpace(d.DisplayName) == "" {
		return nil, fmt.Errorf("%w: project, type and display_name are required", alerting.ErrInvalidParams)
	}
	if oauthTypes[d.Type] {
		return nil, fmt.Errorf("%w: %s", alerting.ErrInvalidParams, slackCreateMsg)
	}
	required, ok := creatableTypes[d.Type]
	if !ok {
		return nil, fmt.Errorf("%w: channel type %q cannot be created through the API; create it in the Google Cloud console, then pick the channel here", alerting.ErrInvalidParams, d.Type)
	}
	labels := map[string]string{}
	for k, v := range d.Labels {
		labels[k] = v
	}
	for k, v := range d.SensitiveLabels {
		labels[k] = v
	}
	for _, k := range required {
		if strings.TrimSpace(labels[k]) == "" {
			return nil, fmt.Errorf("%w: label %q is required for %s channels", alerting.ErrInvalidParams, k, d.Type)
		}
	}
	return &monitoringpb.NotificationChannel{
		Type:        d.Type,
		DisplayName: d.DisplayName,
		Labels:      labels,
		Enabled:     wrapperspb.Bool(true),
	}, nil
}
