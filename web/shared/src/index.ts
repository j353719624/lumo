// Formatting helpers shared by the lumos and lumoc consoles.

export function formatBytes(n: number): string {
  n = Number(n) || 0;
  if (n >= 1024 ** 3) return (n / 1024 ** 3).toFixed(1) + ' GB';
  if (n >= 1024 ** 2) return (n / 1024 ** 2).toFixed(0) + ' MB';
  if (n >= 1024) return (n / 1024).toFixed(0) + ' KB';
  return n + ' B';
}

export function formatRate(n: number): string {
  n = Number(n) || 0;
  if (n >= 1024 ** 2) return (n / 1024 ** 2).toFixed(1) + ' MB/s';
  if (n >= 1024) return (n / 1024).toFixed(0) + ' KB/s';
  return n.toFixed(0) + ' B/s';
}

export function formatUptime(ms: number): string {
  const s = Math.floor(ms / 1000);
  const h = String(Math.floor(s / 3600)).padStart(2, '0');
  const m = String(Math.floor((s % 3600) / 60)).padStart(2, '0');
  const ss = String(s % 60).padStart(2, '0');
  return `${h}:${m}:${ss}`;
}

export function clock(d: Date = new Date()): string {
  return d.toTimeString().slice(0, 8);
}

export * from "./i18n";
