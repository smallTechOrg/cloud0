import { describe, expect, it } from 'vitest';
import { errorCodeChips, formatShare, sampleIsEmpty } from './errorsFormat';
import { errorSamplesFixture } from './fixtures2';

describe('errorCodeChips', () => {
  it('lists only >=400 codes, 5xx first then by count', () => {
    const chips = errorCodeChips({ '200': 9000, '301': 5, '404': 1463, '401': 14, '502': 2, '500': 7, '403': 0 });
    expect(chips.map((c) => c.code)).toEqual(['500', '502', '404', '401']);
    expect(chips.map((c) => c.kind)).toEqual(['5xx', '5xx', '4xx', '4xx']);
  });
  it('is empty for null or no error codes', () => {
    expect(errorCodeChips(null)).toEqual([]);
    expect(errorCodeChips({ '200': 1 })).toEqual([]);
  });
});

describe('formatShare / sampleIsEmpty', () => {
  it('formats a share as a whole percent', () => {
    expect(formatShare(0.934)).toBe('93%');
    expect(formatShare(null)).toBeNull();
  });
  it('distinguishes empty from out-of-retention', () => {
    expect(sampleIsEmpty({ ...errorSamplesFixture, scanned: 0 })).toBe(true);
    expect(sampleIsEmpty({ ...errorSamplesFixture, scanned: 0, out_of_retention: true })).toBe(false);
    expect(sampleIsEmpty(errorSamplesFixture)).toBe(false);
  });
});
