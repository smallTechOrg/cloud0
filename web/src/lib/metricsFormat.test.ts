import { describe, expect, it } from 'vitest';
import { errorsTableFixture, metricsTableFixture } from './fixtures2';
import {
  GROUP_ORDER,
  buildErrorsCsv,
  buildMetricsCsv,
  cellText,
  formatCount,
  formatMetricNumber,
  groupHeading,
  headerUnit,
  limitClassKey,
  metricsCsvFilename,
  orderGroups,
  prettyAggregation,
  resourceLabel,
} from './metricsFormat';
import type { MetricCell, MetricsTableResponse } from './types';

const prov = { source: 's', query: 'q', aggregation: '{}', window: { start: 'a', end: 'b' }, fetched_at: 'c' };
const cell = (over: Partial<MetricCell>): MetricCell => ({ value: null, unit: '%', gap: null, hint: null, provenance: prov, limit_level: null, ...over });

describe('formatting', () => {
  it('formats to 2 dp with grouping and no negative zero', () => {
    expect(formatMetricNumber(12.5)).toBe('12.50');
    expect(formatMetricNumber(15300)).toBe('15,300.00');
    expect(formatMetricNumber(-0.001)).toBe('0.00');
  });

  it('renders gap text, never 0, for gap cells', () => {
    expect(cellText(cell({ gap: 'no agent', hint: 'install the Ops Agent' }))).toBe('no agent');
    expect(cellText(cell({ value: 0 }))).toBe('0.00');
    expect(cellText(cell({}))).toBe('—');
    expect(cellText(undefined)).toBe('—');
  });

  it('maps limit levels to classes (ok = none)', () => {
    expect(limitClassKey('ok')).toBeNull();
    expect(limitClassKey(null)).toBeNull();
    expect(limitClassKey('near')).toBe('limitNear');
    expect(limitClassKey('over')).toBe('limitOver');
  });

  it('keeps sheet group order and headings', () => {
    const shuffled = [...metricsTableFixture.groups].reverse();
    expect(orderGroups(shuffled).map((g) => g.group)).toEqual([...GROUP_ORDER]);
    expect(GROUP_ORDER.map((g) => groupHeading(g))).toEqual(['VMs', 'Cloud SQL', 'Networking', 'Cloud Run', 'Cloud Storage', 'Cloud Logging', 'Errors']);
  });

  it('omits unit line when the label already has it', () => {
    expect(headerUnit('CPU %', '%')).toBeNull();
    expect(headerUnit('Outbound GB', 'GB')).toBeNull();
    expect(headerUnit('Requests', 'count')).toBe('count');
  });

  it('pretty-prints aggregation JSON and passes through non-JSON', () => {
    expect(prettyAggregation('{"a":1}')).toBe('{\n  "a": 1\n}');
    expect(prettyAggregation('not json')).toBe('not json');
  });

  it('formats counts', () => {
    expect(formatCount(null)).toBe('—');
    expect(formatCount(0)).toBe('0');
    expect(formatCount(15300)).toBe('15,300');
  });
});

describe('metrics CSV', () => {
  it('writes sections in order with gap text in cells', () => {
    const csv = buildMetricsCsv(metricsTableFixture);
    const heads = csv.split('\r\n').filter((l) => GROUP_ORDER.map((g) => groupHeading(g)).includes(l));
    expect(heads).toEqual(['VMs', 'Cloud SQL', 'Networking', 'Cloud Run', 'Cloud Storage', 'Cloud Logging', 'Errors']);
    expect(csv).toContain('project-a,vm-a,12.50,no agent,no agent');
    expect(csv).toContain('not collected');
    expect(csv.endsWith('\r\n')).toBe(true);
  });

  it('quotes fields containing commas, quotes and newlines', () => {
    const data: MetricsTableResponse = {
      ...metricsTableFixture,
      groups: [
        {
          group: 'vm',
          label: 'VM',
          columns: [{ key: 'cpu_pct', label: 'CPU %', unit: '%' }],
          rows: [{ project: 'p', resource: 'a,"b"\nc', is_total: false, cells: { cpu_pct: cell({ gap: 'no data, sorry' }) } }],
        },
      ],
    };
    const csv = buildMetricsCsv(data);
    expect(csv).toContain('p,"a,""b""\nc","no data, sorry"');
  });

  it('includes every group TOTAL row, dashes for percentages, and the active/idle columns', () => {
    const csv = buildMetricsCsv(metricsTableFixture);
    const lines = csv.split('\r\n');
    expect(lines.filter((l) => l.startsWith(',Total,'))).toHaveLength(5); // vm, networking, cloudrun, logging, errors
    expect(csv).toContain(',Total,—,—,—');
    expect(csv).toContain('Active memory (GiB-s)');
    expect(csv).toContain('Idle (min-instance) memory (GiB-s)');
    expect(csv).toContain(',Total,9200.00,1200.00,8000.00,15300.00,4100.00,100.00,no data,not collected');
  });

  it('labels the total row "Total" and keeps the legacy project total label', () => {
    expect(resourceLabel('', '', true)).toEqual({ name: 'Total', total: true });
    expect(resourceLabel('p', '')).toEqual({ name: 'p (total)', total: true });
    expect(resourceLabel('p', 'svc')).toEqual({ name: 'svc', total: false });
  });

  it('every fixture group with resource rows ends in exactly one total row', () => {
    for (const g of metricsTableFixture.groups) {
      if (g.rows.length === 0) continue;
      expect(g.rows.filter((r) => r.is_total)).toHaveLength(1);
      expect(g.rows[g.rows.length - 1]?.is_total).toBe(true);
    }
  });

  it('builds a filename', () => {
    expect(metricsCsvFilename('all', '2026-W43')).toBe('cloud0-metrics-all-2026-W43.csv');
    expect(metricsCsvFilename('a/b', '2026-W43')).toBe('cloud0-metrics-a_b-2026-W43.csv');
  });
});

describe('errors CSV', () => {
  it('emits all sources, gaps with hints, and a totals row', () => {
    const lines = buildErrorsCsv(errorsTableFixture).trimEnd().split('\r\n');
    expect(lines[0]).toBe('Source,Project,Service,2xx,3xx,4xx,5xx,Groups,Occurrences,Gap,Fix hint');
    expect(lines[1]).toBe('Cloud Run,project-a,svc-a,1000,0,12,3,,,,');
    expect(lines).toContain('Error Reporting,project-a,svc-a,,,,,3,40,,');
    const lb = lines.find((l) => l.startsWith('Load balancer'));
    expect(lb).toContain('Load balancer,project-a,,,,,,,,logging disabled,');
    expect(lb).toContain('backend service');
    expect(lines[lines.length - 1]).toBe('Total,,,2000,0,1452,3,3,40,,');
  });
});
