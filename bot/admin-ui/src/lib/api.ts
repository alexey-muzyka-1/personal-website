// Общий доступ к данным. Один модуль, потому что срез (источник,
// состояние, период) должен читаться и записываться одинаково на всех
// страницах: иначе клик по источнику на одной странице и на другой дадут
// разные адреса, и ссылкой такой срез не пошлёшь.

const BASE = import.meta.env.BASE_URL.replace(/\/?$/, '/');

/** NO_VALUE — «поле пустое»: пришёл без метки, не ответил на вопрос.
 *  Пустая строка занята под «фильтра нет», поэтому пустоту называем. */
export const NO_VALUE = '-';

export const PERIODS = [
  { days: 7, label: '7 дней' },
  { days: 30, label: '30 дней' },
  { days: 0, label: 'всё время' },
];

/** CHANNEL — отношение к каналу как фильтр. Значения те же слова, что
 *  понимает сервер: список закрыт с обеих сторон. */
export const CHANNEL = [
  { value: 'member', label: 'подписан' },
  { value: 'left', label: 'отписался' },
  { value: NO_VALUE, label: 'не подписан' },
];

export interface Slice {
  source: string;
  stage: string;
  channel: string;
  days: number;
}

export function readSlice(): Slice {
  const q = new URLSearchParams(location.search);
  const days = Number(q.get('days') ?? 0);
  const channel = q.get('channel') ?? '';
  return {
    source: q.get('source') ?? '',
    stage: q.get('stage') ?? '',
    channel: CHANNEL.some((c) => c.value === channel) ? channel : '',
    // Период берётся только из известного списка: адрес с мусором не
    // должен молча показать срез, которого никто не выбирал.
    days: PERIODS.some((p) => p.days === days && p.days !== 0) ? days : 0,
  };
}

export function params(slice: Slice): URLSearchParams {
  const q = new URLSearchParams();
  if (slice.source) q.set('source', slice.source);
  if (slice.stage) q.set('stage', slice.stage);
  if (slice.channel) q.set('channel', slice.channel);
  if (slice.days) q.set('days', String(slice.days));
  return q;
}

/** href — адрес страницы админки с текущим срезом и одним изменённым
 *  параметром. Пустое значение убирает параметр: так же работает крестик
 *  на фильтре и повторный клик по уже выбранной строке. */
export function href(page: string, slice: Slice, patch: Partial<Slice> = {}): string {
  const q = params({ ...slice, ...patch } as Slice);
  const path = BASE + (page ? page + '/' : '');
  return q.toString() ? `${path}?${q}` : path;
}

/** api — чтение JSON у бота. Ошибку не проглатываем: пустая страница без
 *  объяснения хуже честного сообщения. */
export async function api(name: string, extra: Record<string, string> = {}): Promise<any> {
  const q = params(readSlice());
  for (const [k, v] of Object.entries(extra)) q.set(k, v);

  const res = await fetch(`${BASE}api/${name}?${q}`, { headers: { accept: 'application/json' } });
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    throw new Error(body?.error ?? `сервер ответил ${res.status}`);
  }
  return body;
}

/** fail — показать ошибку там, где ждали данные. */
export function fail(host: Element | null, err: unknown): void {
  if (!host) return;
  const message = err instanceof Error ? err.message : String(err);
  host.innerHTML = '';
  const p = document.createElement('p');
  p.className = 'empty';
  p.textContent = message;
  host.append(p);
}

export function share(part: number, whole: number): string {
  if (!whole) return '';
  return Math.round((part * 100) / whole) + '%';
}

export function sourceLabel(id: string): string {
  return id === '' ? 'без метки' : id;
}

/** el — маленький помощник вместо innerHTML со склейкой строк.
 *  Данные приходят от людей (имя в Telegram, метка Reel), и склеивать их
 *  в HTML — это XSS в админке с чужими именами. */
export function el(tag: string, attrs: Record<string, any> = {}, ...kids: any[]): HTMLElement {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') node.className = String(v);
    else if (k === 'text') node.textContent = String(v);
    else node.setAttribute(k, String(v));
  }
  for (const kid of kids.flat()) {
    if (kid === undefined || kid === null || kid === false) continue;
    node.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return node;
}
