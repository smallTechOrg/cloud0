import { describe, expect, it } from 'vitest';
import {
  adoptCandidates,
  buildChannelDraft,
  buildTemplateParams,
  channelWarningLines,
  classifyAlertError,
  coverageSummary,
  filterPolicies,
  formatSeconds,
  groupTemplates,
  initialTemplateForm,
  installedPolicies,
  paramsSummary,
  parseRawPolicy,
  parseUnifiedDiff,
  routingEqual,
  skippedLabel,
  slackGuidance,
  templateGroup,
  toggleName,
  uptimeInstanceDefaults,
  validateRouting,
  verificationLabel,
} from './alertsFormat';
import { channelsFixture, policiesFixture, routingFixture, templatesFixture } from './fixtures3';
import type { AlertTemplate, PolicyView } from './types';

const cpu = templatesFixture.templates[0]!;

describe('classifyAlertError', () => {
  it('maps API codes to drawer classes', () => {
    expect(classifyAlertError('preview_stale')).toBe('stale');
    expect(classifyAlertError('alerts_not_configured')).toBe('not_configured');
    expect(classifyAlertError('permission_denied')).toBe('permission');
    expect(classifyAlertError('invalid_params')).toBe('invalid');
    expect(classifyAlertError('upstream_error')).toBe('other');
  });
});

describe('parseUnifiedDiff', () => {
  it('classifies add/remove/context lines and ignores empty diffs', () => {
    expect(parseUnifiedDiff('')).toEqual([]);
    expect(parseUnifiedDiff('+ a: 1\n- a: 0\n  b: 2').map((l) => l.kind)).toEqual(['add', 'remove', 'ctx']);
  });
});

describe('formatSeconds', () => {
  it('uses the largest exact unit', () => {
    expect(formatSeconds(300)).toBe('5 min');
    expect(formatSeconds(7200)).toBe('2 h');
    expect(formatSeconds(90)).toBe('90 s');
    expect(formatSeconds(0)).toBe('0 s');
  });
});

describe('templates', () => {
  it('groups by area', () => {
    expect(templateGroup('vm_memory_high')).toBe('VM');
    expect(templateGroup('run_5xx_burst')).toBe('Errors');
    expect(templateGroup('lb_5xx_burst')).toBe('Errors');
    expect(templateGroup('sql_down')).toBe('Database');
    expect(templateGroup('uptime_down')).toBe('Uptime');
    expect(groupTemplates(templatesFixture.templates).map((g) => g.group)).toEqual(['VM']);
  });
  it('summarises defaults and coverage', () => {
    expect(paramsSummary(cpu, cpu.defaults)).toBe('CPU % 80 % · For 5 min');
    expect(coverageSummary(cpu)).toEqual({ covered: 1, total: 1, skipped: [] });
    const t: AlertTemplate = {
      ...cpu,
      covered_resources: [
        { project: 'proj-a', resource_type: 'vm', name: 'vm-a', covered: false, note: 'no Ops Agent → CPU only' },
        { project: 'proj-a', resource_type: 'vm', name: 'vm-b', covered: true, note: '' },
      ],
    };
    const c = coverageSummary(t);
    expect(c.covered).toBe(1);
    expect(skippedLabel(c.skipped[0]!)).toBe('vm-a: no Ops Agent → CPU only');
  });
  it('finds installed managed policies', () => {
    const managed: PolicyView = { ...policiesFixture.policies[0]!, managed: true, template_id: 'vm_cpu_high' };
    expect(installedPolicies([managed, ...policiesFixture.policies], 'vm_cpu_high')).toHaveLength(1);
    expect(installedPolicies(policiesFixture.policies, 'vm_cpu_high')).toHaveLength(0);
  });
  it('uptime instance defaults follow the frozen escalation', () => {
    expect(uptimeInstanceDefaults('warning')).toEqual({ duration_seconds: 600, severity: 'WARNING' });
    expect(uptimeInstanceDefaults('critical')).toEqual({ duration_seconds: 7200, severity: 'CRITICAL' });
  });
});

describe('buildTemplateParams', () => {
  it('builds params from defaults and carries enabled', () => {
    const r = buildTemplateParams(cpu, initialTemplateForm(cpu));
    expect(r).toEqual({ ok: true, value: { threshold: 80, duration_seconds: 300, severity: 'WARNING', enabled: true } });
  });
  it('rejects out-of-range, non-integer and non-multiple-of-60 values with a named message', () => {
    const f = initialTemplateForm(cpu);
    expect(buildTemplateParams(cpu, { ...f, threshold: '150' })).toEqual({ ok: false, error: 'CPU % must be at most 100 %.' });
    expect(buildTemplateParams(cpu, { ...f, threshold: '0' }).ok).toBe(false);
    expect(buildTemplateParams(cpu, { ...f, threshold: 'abc' })).toEqual({ ok: false, error: 'CPU % must be a number.' });
    expect(buildTemplateParams(cpu, { ...f, duration_seconds: '90.5' })).toEqual({ ok: false, error: 'For must be a whole number.' });
    expect(buildTemplateParams(cpu, { ...f, duration_seconds: '90' })).toEqual({ ok: false, error: 'For must be a multiple of 60 seconds.' });
    expect(buildTemplateParams(cpu, { ...f, threshold: '' })).toEqual({ ok: false, error: 'CPU % is required.' });
    expect(buildTemplateParams(cpu, { ...f, severity: 'LOUD' }).ok).toBe(false);
  });
  it('includes the channel override only when chosen and adds severity when not a spec', () => {
    const noSev: AlertTemplate = { ...cpu, param_specs: cpu.param_specs.filter((s) => s.key !== 'severity') };
    const f = { ...initialTemplateForm(noSev), channel_override: ['projects/proj-a/notificationChannels/111'], enabled: false };
    expect(buildTemplateParams(noSev, f)).toEqual({
      ok: true,
      value: { threshold: 80, duration_seconds: 300, severity: 'WARNING', enabled: false, channel_override: ['projects/proj-a/notificationChannels/111'] },
    });
  });
});

describe('policies', () => {
  it('filters managed vs unmanaged', () => {
    const managed: PolicyView = { ...policiesFixture.policies[0]!, name: 'x', managed: true };
    const all = [managed, ...policiesFixture.policies];
    expect(filterPolicies(all, 'managed')).toHaveLength(1);
    expect(filterPolicies(all, 'unmanaged')).toHaveLength(policiesFixture.policies.length);
    expect(filterPolicies(all, 'all')).toHaveLength(all.length);
  });
  it('parses raw JSON objects only', () => {
    expect(parseRawPolicy('{"a":1}')).toEqual({ ok: true, value: { a: 1 } });
    expect(parseRawPolicy('[1]').ok).toBe(false);
    expect(parseRawPolicy('{oops').ok).toBe(false);
  });
});

describe('adoptCandidates', () => {
  it('folds the two uptime shapes into one entry and ignores managed policies', () => {
    const base = policiesFixture.policies[0]!;
    const mk = (name: string, shape: PolicyView['adopt_shape'], managed = false): PolicyView => ({ ...base, name, adopt_shape: shape, managed });
    const c = adoptCandidates([mk('a', 'uptime_backend_down'), mk('b', 'uptime_down_2h'), mk('c', 'zero_cpu_spike'), mk('d', 'high_memory_load', true)]);
    expect(c.map((x) => x.info.shape)).toEqual(['zero_cpu_spike', 'uptime_backend_down']);
    expect(c[1]!.policies).toHaveLength(2);
  });
});

describe('channels and routing', () => {
  const ch = channelsFixture.channels[0]!;
  it('labels verification and warns for unverified email', () => {
    expect(verificationLabel(ch)).toBe('unverified');
    expect(verificationLabel({ ...ch, verification_status: 'VERIFIED' })).toBe('verified');
    expect(verificationLabel({ ...ch, verification_status: 'VERIFICATION_STATUS_UNSPECIFIED' })).toBe('unverified (status unset)');
    expect(channelWarningLines([ch])).toEqual(['email channel “Alert” unverified']);
    expect(channelWarningLines([{ ...ch, verification_status: 'VERIFIED', warnings: [] }])).toEqual([]);
  });
  it('validates routing and detects changes', () => {
    expect(validateRouting(routingFixture.routing).ok).toBe(true);
    expect(validateRouting({ ...routingFixture.routing, ERROR: [] })).toEqual({ ok: false, error: 'error needs at least one channel.' });
    expect(routingEqual(routingFixture.routing, { ...routingFixture.routing })).toBe(true);
    expect(routingEqual(routingFixture.routing, { ...routingFixture.routing, WARNING: [] })).toBe(false);
    expect(toggleName(['a'], 'b')).toEqual(['a', 'b']);
    expect(toggleName(['a', 'b'], 'a')).toEqual(['b']);
  });
  it('gives Slack guidance only when it is not creatable', () => {
    expect(slackGuidance(channelsFixture.descriptors)).toContain('console OAuth');
    expect(slackGuidance([])).toBeNull();
  });
  it('builds a channel draft separating sensitive labels', () => {
    const webhook = { type: 'webhook_tokenauth', display_name: 'Webhook', creatable: true, hint: '', labels: [{ key: 'url', description: 'Webhook URL', required: true, sensitive: true }] };
    expect(buildChannelDraft('proj-a', webhook, 'hook', { url: ' https://example.com/h ' })).toEqual({
      ok: true,
      value: { project: 'proj-a', type: 'webhook_tokenauth', display_name: 'hook', labels: {}, sensitive_labels: { url: 'https://example.com/h' } },
    });
    expect(buildChannelDraft('proj-a', webhook, '', {}).ok).toBe(false);
    expect(buildChannelDraft('proj-a', webhook, 'hook', {})).toEqual({ ok: false, error: 'Webhook URL is required.' });
  });
});
