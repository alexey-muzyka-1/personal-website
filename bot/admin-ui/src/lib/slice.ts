// Полоса управления срезом: период, активные фильтры, выгрузка.
// Одна на все страницы — иначе «этот источник за неделю» на разных
// страницах означало бы разное.
import { PERIODS, NO_VALUE, CHANNEL, href, params, readSlice, el, type Slice } from './api.ts';

const BASE = import.meta.env.BASE_URL.replace(/\/?$/, '/');

function icon(paths: string[], width = 13): SVGSVGElement {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('width', String(width));
  svg.setAttribute('height', String(width));
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('fill', 'none');
  svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '2');
  svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round');
  svg.setAttribute('aria-hidden', 'true');
  for (const d of paths) {
    const p = document.createElementNS(ns, 'path');
    p.setAttribute('d', d);
    svg.append(p);
  }
  return svg;
}

export const ICON = {
  clear: () => icon(['M18 6 6 18', 'm6 6 12 12']),
  download: () => icon(['M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4', 'M7 10l5 5 5-5', 'M12 15V3']),
  back: () => icon(['M19 12H5', 'm12 19-7-7 7-7'], 14),
};

/** stageLabel держим и на клиенте: подписи состояний нужны фильтру-чипу
 *  до того, как придёт первый ответ сервера. */
export function stageLabel(v: string): string {
  switch (v) {
    case 'not_shipping': return 'не выпускает стабильно';
    case 'no_signal': return 'выпускает, не видит сигнала';
    case 'other': return 'другая ситуация';
    case NO_VALUE: return 'не ответил';
    case '': return 'не ответил';
    default: return v;
  }
}

/** channelLabel — подпись фильтра по каналу. Со словом «канал» внутри
 *  самой подписи: чип «отписался» без него читается как состояние
 *  человека, а не как отношение к каналу. */
export function channelLabel(v: string): string {
  const found = CHANNEL.find((c) => c.value === v);
  return found ? `канал: ${found.label}` : `канал: ${v}`;
}

/** renderSlice рисует полосу управления. page — на какую страницу
 *  возвращают ссылки периодов и фильтров, чтобы срез менялся, не уводя
 *  человека с той страницы, где он сейчас. */
export function renderSlice(host: Element | null, page: string, slice: Slice = readSlice()): void {
  if (!host) return;
  host.innerHTML = '';

  for (const p of PERIODS) {
    host.append(
      el('a', {
        class: 'pill',
        href: href(page, slice, { days: p.days }),
        'aria-current': p.days === slice.days ? 'true' : 'false',
        text: p.label,
      }),
    );
  }

  if (slice.source) {
    const label = slice.source === NO_VALUE ? 'без метки' : slice.source;
    host.append(
      el('a', { class: 'pill chip', href: href(page, slice, { source: '' }) },
        `источник: ${label}`, ICON.clear()),
    );
  }
  if (slice.stage) {
    host.append(
      el('a', { class: 'pill chip', href: href(page, slice, { stage: '' }) },
        `состояние: ${stageLabel(slice.stage)}`, ICON.clear()),
    );
  }
  if (slice.channel) {
    host.append(
      el('a', { class: 'pill chip', href: href(page, slice, { channel: '' }) },
        channelLabel(slice.channel), ICON.clear()),
    );
  }

  const right = el('span', { class: 'right' });
  // Выгрузка идёт тем же срезом, что на экране: собираем адрес из того же
  // места, иначе однажды на странице будет один фильтр, а в файле другой.
  const q = params(slice);
  const link = `${BASE}export.xlsx${q.toString() ? '?' + q : ''}`;
  right.append(el('a', { class: 'pill', href: link, download: '' }, ICON.download(), ' Excel'));
  host.append(right);
}
