import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { adoptFixture, alertsStatusFixture, applyFixture, channelsFixture, policiesFixture, previewFixture, routingFixture, templatesFixture } from '@/lib/fixtures3';
import type { AlertPreview, PolicyView } from '@/lib/types';
import AdoptBanner from './AdoptBanner';
import { ChannelsSection, StatusStrip } from './AlertsPanel';
import PolicyTable from './PolicyTable';
import { PreviewApplyView, type DrawerError, type PreviewApplyViewProps } from './PreviewApplyDrawer';
import RoutingEditor from './RoutingEditor';
import TemplateCard from './TemplateCard';

const noop = () => undefined;
const cpu = templatesFixture.templates[0]!;

function drawer(over: Partial<PreviewApplyViewProps> = {}): string {
  return renderToStaticMarkup(
    createElement(PreviewApplyView, {
      phase: 'preview',
      preview: previewFixture,
      result: null,
      error: null,
      applying: false,
      confirm: false,
      onConfirm: noop,
      onApply: noop,
      onRepreview: noop,
      onClose: noop,
      ...over,
    }),
  );
}

const err = (cls: DrawerError['cls'], code: string, hint: string | null = null): DrawerError => ({ cls, code, message: 'boom', hint });

describe('PreviewApplyView', () => {
  it('shows diff, changed paths, desired JSON, gcloud with copy, and an enabled Apply', () => {
    const html = drawer();
    expect(html).toContain('data-testid="preview-diff"');
    expect(html).toContain('+ displayName: cloud0: VM CPU high');
    expect(html).toContain('data-testid="preview-changes"');
    expect(html).toContain('<code>displayName</code>');
    expect(html).toContain('data-testid="preview-desired"');
    expect(html).toContain('&quot;managed-by&quot;: &quot;cloud0&quot;');
    expect(html).toContain('gcloud alpha monitoring policies create');
    expect(html).toContain('data-testid="preview-gcloud-copy"');
    expect(html).toMatch(/data-testid="preview-apply"(?![^>]*disabled)/);
    expect(html).not.toContain('data-testid="preview-confirm"');
  });
  it('renders preview warnings', () => {
    const html = drawer({ preview: { ...previewFixture, warnings: ['No notification channel resolved for severity ERROR in proj-a: the policy would notify nobody.'] } });
    expect(html).toContain('data-testid="preview-warnings"');
    expect(html).toContain('would notify nobody');
  });
  it('requires a confirm checkbox for deletes and disables Apply until ticked', () => {
    const del: AlertPreview = { ...previewFixture, action: 'delete', target: 'projects/proj-a/alertPolicies/333', desired: null, requires_confirm: true };
    const off = drawer({ preview: del });
    expect(off).toContain('data-testid="preview-confirm"');
    expect(off).toMatch(/data-testid="preview-apply" disabled/);
    expect(off).toContain('Apply delete');
    const on = drawer({ preview: del, confirm: true });
    expect(on).not.toMatch(/data-testid="preview-apply" disabled/);
  });
  it('shows the loading state', () => {
    const html = drawer({ phase: 'loading', preview: null });
    expect(html).toContain('data-testid="preview-loading"');
    expect(html).not.toContain('data-testid="preview-apply"');
  });
  it('409 preview_stale: explains and offers a re-preview; Apply is disabled', () => {
    const html = drawer({ error: err('stale', 'preview_stale') });
    expect(html).toContain('Live state changed');
    expect(html).toContain('data-testid="preview-repreview"');
    expect(html).toMatch(/data-testid="preview-apply" disabled/);
  });
  it('503 alerts_not_configured: names CLOUD0_ALERTS_CREDENTIALS', () => {
    const html = drawer({ error: err('not_configured', 'alerts_not_configured') });
    expect(html).toContain('data-testid="preview-not-configured"');
    expect(html).toContain('CLOUD0_ALERTS_CREDENTIALS');
  });
  it('403 permission_denied: shows the role hint and keeps the preview open', () => {
    const html = drawer({ error: err('permission', 'permission_denied', 'Grant roles/monitoring.alertPolicyEditor to the cloud0-alerts service account on proj-a') });
    expect(html).toContain('data-testid="preview-permission-hint"');
    expect(html).toContain('roles/monitoring.alertPolicyEditor');
    expect(html).toContain('data-testid="preview-diff"');
  });
  it('preview-phase failure shows the error and a retry, no Apply', () => {
    const html = drawer({ phase: 'error', preview: null, error: err('other', 'upstream_error') });
    expect(html).toContain('upstream_error');
    expect(html).toContain('data-testid="preview-retry"');
    expect(html).not.toContain('data-testid="preview-apply"');
  });
  it('read-only mode disables Apply with the reason', () => {
    const html = drawer({ applyDisabledReason: 'Read-only: set CLOUD0_ALERTS_CREDENTIALS.' });
    expect(html).toMatch(/data-testid="preview-apply" disabled/);
    expect(html).toContain('Read-only: set CLOUD0_ALERTS_CREDENTIALS.');
  });
  it('success shows the change-history id', () => {
    const html = drawer({ phase: 'applied', result: applyFixture });
    expect(html).toContain('data-testid="preview-success"');
    expect(html).toContain('#12');
    expect(html).not.toContain('data-testid="preview-apply"');
    expect(html).toContain('Done');
  });
});

describe('TemplateCard', () => {
  const card = (over: Partial<Parameters<typeof TemplateCard>[0]> = {}) =>
    renderToStaticMarkup(createElement(TemplateCard, { template: cpu, installed: [], projects: ['proj-a'], channels: channelsFixture.channels, onPreview: noop, ...over }));

  it('shows title, severity chip, default vs current, coverage and an Install button', () => {
    const html = card();
    expect(html).toContain('VM CPU high');
    expect(html).toContain('data-severity="WARNING"');
    expect(html).toContain('CPU % 80 % · For 5 min');
    expect(html).toContain('not installed');
    expect(html).toContain('1 of 1 resource');
    expect(html).toContain('data-testid="template-install-vm_cpu_high"');
  });
  it('lists skipped resources with their notes', () => {
    const t = { ...cpu, covered_resources: [{ project: 'proj-a', resource_type: 'vm' as const, name: 'vm-a', covered: false, note: 'no Ops Agent → CPU only' }] };
    const html = card({ template: t });
    expect(html).toContain('vm-a: no Ops Agent → CPU only');
    expect(html).toContain('0 of 1 resource');
  });
  it('generates the install form from ParamSpecs with bounds and an enum select', () => {
    const html = card({ initialForm: { mode: 'install' } });
    expect(html).toContain('data-testid="template-form-vm_cpu_high"');
    expect(html).toMatch(/data-testid="param-threshold"[^>]*min="1"[^>]*max="100"/);
    expect(html).toMatch(/data-testid="param-duration_seconds"[^>]*min="60"[^>]*max="86400"/);
    expect(html).toContain('data-testid="param-severity"');
    expect(html).toContain('<option value="CRITICAL">CRITICAL</option>');
    expect(html).toContain('data-testid="param-enabled"');
    expect(html).toContain('data-testid="param-channel-Alert"');
    expect(html).toContain('Preview change');
  });
  it('installed templates show Edit, Disable and Delete…, and no Install', () => {
    const managed: PolicyView = { ...policiesFixture.policies[0]!, name: 'projects/proj-a/alertPolicies/333', display_name: 'cloud0: VM CPU high', managed: true, template_id: 'vm_cpu_high' };
    const html = card({ installed: [managed], template: { ...cpu, params: cpu.defaults, installed: [managed.name] } });
    expect(html).toContain('data-testid="template-edit-projects/proj-a/alertPolicies/333"');
    expect(html).toContain('Disable');
    expect(html).toContain('Delete…');
    expect(html).not.toContain('template-install-vm_cpu_high');
    expect(html).toContain('installed');
  });
});

describe('PolicyTable', () => {
  it('renders managed vs unmanaged badges, severity, channels, warnings and Edit raw only for unmanaged', () => {
    const managed: PolicyView = { ...policiesFixture.policies[0]!, name: 'projects/proj-a/alertPolicies/333', display_name: 'cloud0: VM CPU high', managed: true, template_id: 'vm_cpu_high', severity: 'WARNING', adopt_shape: '', warnings: [] };
    const html = renderToStaticMarkup(createElement(PolicyTable, { policies: [...policiesFixture.policies, managed], onPreview: noop }));
    expect(html).toContain('data-managed="false"');
    expect(html).toContain('>unmanaged<');
    expect(html).toContain('>managed<');
    expect(html).toContain('Covers one VM only');
    expect(html).toContain('data-testid="policy-edit-raw-projects/proj-a/alertPolicies/222"');
    expect(html).not.toContain('policy-edit-raw-projects/proj-a/alertPolicies/333');
    expect(html).toContain('>unset<');
    expect(html).toContain('Alert');
  });
  it('opens the raw JSON editor for an unmanaged policy', () => {
    const html = renderToStaticMarkup(createElement(PolicyTable, { policies: policiesFixture.policies, onPreview: noop, initialRawFor: policiesFixture.policies[0]!.name }));
    expect(html).toContain('data-testid="policy-raw-text"');
    expect(html).toContain('Zero-Backend CPU Spike');
  });
  it('shows an empty state', () => {
    const html = renderToStaticMarkup(createElement(PolicyTable, { policies: [], onPreview: noop }));
    expect(html).toContain('data-testid="policies-empty"');
  });
});

describe('RoutingEditor, ChannelsSection, StatusStrip, AdoptBanner', () => {
  it('routing editor renders three severities with verification warnings', () => {
    const html = renderToStaticMarkup(createElement(RoutingEditor, { data: routingFixture, onSaved: noop }));
    for (const s of ['CRITICAL', 'ERROR', 'WARNING']) expect(html).toContain(`data-testid="routing-${s}"`);
    expect(html).toContain('data-testid="routing-unverified"');
    expect(html).toContain('data-testid="routing-warning"');
    expect(html).toContain('Defaults: every severity');
  });
  it('routing editor shows an inline error', () => {
    const html = renderToStaticMarkup(createElement(RoutingEditor, { data: routingFixture, onSaved: noop, initialError: 'invalid_params — routing.ERROR: unknown channel' }));
    expect(html).toContain('data-testid="routing-error"');
    expect(html).toContain('unknown channel');
  });
  it('channels list is masked and shows the Slack guidance', () => {
    const html = renderToStaticMarkup(createElement(ChannelsSection, { data: channelsFixture, onPreview: noop }));
    expect(html).toContain('a***@example.com');
    expect(html).toContain('data-testid="channels-slack-hint"');
    expect(html).toContain('console OAuth');
    expect(html).toContain('Add channel');
  });
  it('status strip shows writable/read-only and channel warnings', () => {
    const ro = renderToStaticMarkup(createElement(StatusStrip, { status: { ...alertsStatusFixture, alerts_configured: false }, channels: channelsFixture.channels, policies: policiesFixture }));
    expect(ro).toContain('data-writable="false"');
    expect(ro).toContain('CLOUD0_ALERTS_CREDENTIALS');
    expect(ro).toContain('email channel “Alert” unverified');
    const rw = renderToStaticMarkup(createElement(StatusStrip, { status: alertsStatusFixture, channels: [], policies: null }));
    expect(rw).toContain('data-writable="true"');
  });
  it('adopt banner explains the memory 0-second bug plainly and links the budget to the Budgets tab', () => {
    const mem: PolicyView = { ...policiesFixture.policies[0]!, name: 'projects/proj-a/alertPolicies/444', display_name: 'High memory load', adopt_shape: 'high_memory_load' };
    const html = renderToStaticMarkup(createElement(AdoptBanner, { policies: [...policiesFixture.policies, mem], onReview: noop }));
    expect(html).toContain('data-testid="adopt-zero_cpu_spike"');
    expect(html).toContain('duration is 0 seconds');
    expect(html).toContain('Budgets tab');
    expect(html).toContain('data-testid="adopt-preview-zero_cpu_spike"');
  });
  it('adopt banner lists preview steps and the follow-up after everything is applied', () => {
    const html = renderToStaticMarkup(
      createElement(AdoptBanner, {
        policies: policiesFixture.policies,
        onReview: noop,
        initialStates: { zero_cpu_spike: { phase: 'ready', data: adoptFixture, applied: [0], followed: [] } },
      }),
    );
    expect(html).toContain('cloud0: VM CPU high');
    expect(html).toContain('Disable old policy…');
    expect(html).toContain('Replacement covers every VM');
  });
});
