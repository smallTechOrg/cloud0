package monclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"github.com/smallTechOrg/cloud0/internal/alerting"
	labelpb "google.golang.org/genproto/googleapis/api/label"
)

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"alerts@example.com": "al***@example.com",
		"a@example.com":      "a***@example.com",
		"ab@example.com":     "a***@example.com",
		"":                   "***",
		"not-an-address":     "***",
		"@example.com":       "***",
	}
	for in, want := range cases {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChannelToViewMasksAndWarns(t *testing.T) {
	v := channelToView(&monitoringpb.NotificationChannel{
		Name: "projects/proj-a/notificationChannels/111", Type: "email", DisplayName: "Alert",
		Labels:             map[string]string{"email_address": "alerts@example.com"},
		VerificationStatus: monitoringpb.NotificationChannel_UNVERIFIED,
	})
	if v.Project != "proj-a" || v.Address != "al***@example.com" || v.VerificationStatus != "UNVERIFIED" || !v.Enabled {
		t.Fatalf("view = %+v", v)
	}
	if len(v.Warnings) != 1 || !strings.Contains(v.Warnings[0], "not verified") {
		t.Fatalf("warnings = %v", v.Warnings)
	}
	verified := channelToView(&monitoringpb.NotificationChannel{Type: "email", Labels: map[string]string{"email_address": "alerts@example.com"}, VerificationStatus: monitoringpb.NotificationChannel_VERIFIED})
	if len(verified.Warnings) != 0 {
		t.Fatalf("verified email must not warn: %v", verified.Warnings)
	}
	hook := channelToView(&monitoringpb.NotificationChannel{Type: "webhook_tokenauth", Labels: map[string]string{"url": "https://secret.example.invalid/hook?k=SECRET"}})
	if hook.Address != "" {
		t.Fatalf("webhook address must be empty, got %q", hook.Address)
	}
}

func TestDescriptorToViewCreatable(t *testing.T) {
	mk := func(typ string, keys ...string) *monitoringpb.NotificationChannelDescriptor {
		d := &monitoringpb.NotificationChannelDescriptor{Type: typ, DisplayName: typ}
		for _, k := range keys {
			d.Labels = append(d.Labels, &labelpb.LabelDescriptor{Key: k, Description: k})
		}
		return d
	}
	if d := descriptorToView(mk("email", "email_address")); !d.Creatable || d.Labels[0].Sensitive {
		t.Errorf("email = %+v", d)
	}
	d := descriptorToView(mk("webhook_tokenauth", "url"))
	if !d.Creatable || !d.Labels[0].Sensitive {
		t.Errorf("webhook = %+v", d)
	}
	if s := descriptorToView(mk("slack", "channel_name")); s.Creatable || s.Hint != SlackHint || len(s.Labels) != 0 {
		t.Errorf("slack = %+v", s)
	}
	if g := descriptorToView(mk("campfire")); g.Creatable || g.Hint == "" {
		t.Errorf("campfire = %+v", g)
	}
}

func TestChannelDraftValidation(t *testing.T) {
	_, err := channelDraftToPB(alerting.ChannelDraft{Project: "p", Type: "slack", DisplayName: "cloud0-test-x"})
	if !errors.Is(err, alerting.ErrInvalidParams) || !strings.Contains(err.Error(), "authorize Slack in the Google Cloud console, then pick the channel here") {
		t.Fatalf("slack err = %v", err)
	}
	for _, d := range []alerting.ChannelDraft{
		{Type: "email", DisplayName: "x", Labels: map[string]string{"email_address": "a@example.com"}},
		{Project: "p", Type: "email", DisplayName: "x"},
		{Project: "p", Type: "webhook_tokenauth", DisplayName: "x"},
		{Project: "p", Type: "nonsense", DisplayName: "x"},
	} {
		if _, err := channelDraftToPB(d); !errors.Is(err, alerting.ErrInvalidParams) {
			t.Errorf("draft %+v err = %v", d.Type, err)
		}
	}
	pb, err := channelDraftToPB(alerting.ChannelDraft{Project: "p", Type: "webhook_tokenauth", DisplayName: "cloud0-test-x",
		Labels: map[string]string{"username": "u"}, SensitiveLabels: map[string]string{"url": "https://x.example.invalid/h"}})
	if err != nil || pb.Labels["url"] == "" || pb.Labels["username"] != "u" || !pb.Enabled.GetValue() {
		t.Fatalf("pb = %v, %v", pb, err)
	}
	// error messages never echo label values
	_, err = channelDraftToPB(alerting.ChannelDraft{Project: "p", Type: "webhook_basicauth", DisplayName: "x", SensitiveLabels: map[string]string{"url": "https://leak.example.invalid/SECRET"}})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err must not leak values: %v", err)
	}
}

func TestChannelDeleteValidatesName(t *testing.T) {
	c := &Channels{write: nil}
	if err := c.Delete(context.Background(), "bad"); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestRealListChannelsAndDescriptors(t *testing.T) {
	projects := firstProject(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c, err := NewChannels(ctx, Creds{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	total := 0
	for _, proj := range projects {
		chs, err := c.List(ctx, proj)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, ch := range chs {
			total++
			if ch.Name == "" || ch.Type == "" || ch.Project != proj || ch.VerificationStatus == "" || ch.Warnings == nil {
				t.Fatalf("incomplete channel %+v", ch)
			}
			if ch.Type == "email" && (!strings.Contains(ch.Address, "***") || strings.Count(ch.Address, "@") != 1) {
				t.Fatalf("email address not masked: %q", ch.Address)
			}
			if ch.Type != "email" && ch.Address != "" {
				t.Fatalf("non-email address must be empty")
			}
		}
	}
	if total < 1 {
		t.Fatalf("expected at least 1 channel, got %d", total)
	}
	ds, err := c.Descriptors(ctx, projects[0])
	if err != nil {
		t.Fatalf("Descriptors: %v", err)
	}
	byType := map[string]alerting.ChannelDescriptor{}
	for _, d := range ds {
		byType[d.Type] = d
	}
	if e, ok := byType["email"]; !ok || !e.Creatable || len(e.Labels) == 0 {
		t.Fatalf("email descriptor = %+v", e)
	}
	if s, ok := byType["slack"]; ok && (s.Creatable || s.Hint == "") {
		t.Fatalf("slack descriptor must be non-creatable with hint: %+v", s)
	}
	if w, ok := byType["webhook_tokenauth"]; !ok || !w.Creatable {
		t.Fatalf("webhook_tokenauth descriptor = %+v", w)
	}
}

func TestRealChannelWriteLifecycle(t *testing.T) {
	projects := firstProject(t)
	if strings.TrimSpace(os.Getenv(EnvAlertsCredentials)) == "" {
		t.Skip("CLOUD0_ALERTS_CREDENTIALS not set — real channel write tests need the cloud0-alerts key")
	}
	project := projects[0]
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := NewChannels(ctx, CredsFromEnv())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	display := fmt.Sprintf("%schannel-%d", alerting.TestPrefix, time.Now().UnixNano())
	ch, err := c.Create(ctx, alerting.ChannelDraft{
		Project: project, Type: "webhook_tokenauth", DisplayName: display,
		SensitiveLabels: map[string]string{"url": "https://cloud0-test.example.invalid/hook"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()
		if err := c.Delete(cctx, ch.Name); err != nil && !errors.Is(err, alerting.ErrNotFound) {
			t.Errorf("LOUD: cleanup failed to delete test channel %s: %v", ch.Name, err)
		}
	})
	if ch.DisplayName != display || ch.Type != "webhook_tokenauth" || ch.Address != "" {
		t.Fatalf("created = %+v", ch)
	}
	chs, err := c.List(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range chs {
		found = found || x.Name == ch.Name
	}
	if !found {
		t.Fatal("created channel missing from List")
	}
	if err := c.Delete(ctx, ch.Name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	chs, _ = c.List(ctx, project)
	for _, x := range chs {
		if x.Name == ch.Name {
			t.Fatal("channel still listed after delete")
		}
	}
}
