import { describe, expect, it } from 'vitest';
import { formatBytes, formatRate, formatUptime } from './index';

describe('formatBytes', () => {
  it('scales units', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(2048)).toBe('2 KB');
    expect(formatBytes(5 * 1024 ** 2)).toBe('5 MB');
    expect(formatBytes(1.5 * 1024 ** 3)).toBe('1.5 GB');
  });
});

describe('formatRate', () => {
  it('scales units', () => {
    expect(formatRate(512)).toBe('512 B/s');
    expect(formatRate(2048)).toBe('2 KB/s');
  });
});

describe('formatUptime', () => {
  it('pads segments', () => {
    expect(formatUptime(3661000)).toBe('01:01:01');
  });
});
