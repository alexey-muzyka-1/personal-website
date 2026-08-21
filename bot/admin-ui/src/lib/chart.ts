// График по дням. Рисуется руками в SVG, без библиотеки: нужен один тип
// графика, а любая библиотека — это сотни килобайт и своя вёрстка,
// которую потом придётся уговаривать выглядеть как остальная страница.
import { el } from './api.ts';

const NS = 'http://www.w3.org/2000/svg';

export interface Day {
  date: string;
  people: number;
  opened: number;
  waitlist: number;
}

function node(tag: string, attrs: Record<string, string | number>): SVGElement {
  const n = document.createElementNS(NS, tag);
  for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, String(v));
  return n;
}

function human(date: string): string {
  const [, m, d] = date.split('-');
  return `${d}.${m}`;
}

/** renderDaily — столбики по дням: пришло всего и сколько из них открыло
 *  разбор. Итог виден и в таблице; график отвечает на другой вопрос —
 *  набрано это равномерно или одним днём. */
export function renderDaily(host: Element | null, days: Day[]): void {
  if (!host) return;
  host.innerHTML = '';

  if (!days.length) {
    host.append(el('p', { class: 'empty', text: 'Пока не с чего строить: никто не приходил.' }));
    return;
  }

  const max = Math.max(1, ...days.map((d) => d.people));
  const W = 900;
  const H = 180;
  const padL = 28;
  const padB = 22;
  const inner = W - padL - 8;
  const step = inner / days.length;
  const barW = Math.max(2, Math.min(38, step - 6));

  const svg = node('svg', {
    viewBox: `0 0 ${W} ${H}`,
    width: '100%',
    height: 'auto',
    role: 'img',
    'aria-label': `Приходы по дням, максимум ${max} за день`,
  });

  // Сетка по целым значениям и без повторов: при максимуме в единицу
  // половина округлялась к той же единице, и шкала читалась как «1 1 0».
  const ticks = [...new Set([0, Math.round(max / 2), max])].sort((a, b) => a - b);
  for (const tick of ticks) {
    const frac = tick / max;
    const y = 8 + (H - padB - 8) * (1 - frac);
    svg.append(node('line', {
      x1: padL, x2: W - 4, y1: y, y2: y,
      stroke: 'var(--line)', 'stroke-width': 1,
    }));
    const label = node('text', {
      x: 0, y: y + 4, fill: 'var(--muted)', 'font-size': 10,
    });
    label.textContent = String(tick);
    svg.append(label);
  }

  days.forEach((d, i) => {
    const x = padL + i * step + (step - barW) / 2;
    const full = (H - padB - 8) * (d.people / max);
    const opened = (H - padB - 8) * (d.opened / max);

    const g = node('g', {});
    const title = node('title', {});
    title.textContent = `${human(d.date)}: пришло ${d.people}, открыли ${d.opened}, записались ${d.waitlist}`;
    g.append(title);

    g.append(node('rect', {
      x, y: H - padB - full, width: barW, height: Math.max(full, d.people ? 2 : 0),
      // Подмешиваем к фону, а не к прозрачности: на тёмной теме
      // полупрозрачный акцент выглядел тенью, а не «светлым».
      rx: 3, fill: 'color-mix(in srgb, var(--accent) 38%, var(--bg))',
    }));
    if (d.opened) {
      g.append(node('rect', {
        x, y: H - padB - opened, width: barW, height: Math.max(opened, 2),
        rx: 3, fill: 'var(--accent)',
      }));
    }
    svg.append(g);

    // Подпись не у каждого столбика: на месяце они слипаются в кашу.
    const every = Math.ceil(days.length / 12);
    if (i % every === 0 || i === days.length - 1) {
      const t = node('text', {
        x: x + barW / 2, y: H - 6,
        fill: 'var(--muted)', 'font-size': 10, 'text-anchor': 'middle',
      });
      t.textContent = human(d.date);
      svg.append(t);
    }
  });

  host.append(svg);
  host.append(
    el('p', { class: 'lede' },
      'Бледным — сколько человек пришло, плотным — сколько из них открыло разбор. ',
      'Точные числа по наведению.'),
  );
}

export interface ChannelDay {
  date: string;
  joined: number;
  left: number;
  members: number;
}

/** renderChannel — движение канала по дням: подписки вверх, отписки вниз,
 *  линией размер.
 *
 *  Отписки рисуются вниз от нуля, а не вычитаются из подписок. Чистый
 *  прирост «плюс один» выглядит одинаково при нуле ушедших и при десяти
 *  ушедших из одиннадцати, а это два совершенно разных дня. */
export function renderChannel(host: Element | null, days: ChannelDay[]): void {
  if (!host) return;
  host.innerHTML = '';

  if (!days.length) {
    host.append(el('p', { class: 'empty', text: 'Пока не с чего строить: замер только начался.' }));
    return;
  }

  const max = Math.max(1, ...days.map((d) => Math.max(d.joined, d.left)));
  const W = 900;
  const H = 200;
  const padL = 28;
  const padB = 22;
  // Ноль не по центру: отписок обычно меньше, чем подписок, и половина
  // холста под них — это половина холста пустоты.
  const zero = 8 + (H - padB - 8) * 0.62;
  const up = zero - 8;
  const down = H - padB - zero;
  const inner = W - padL - 8;
  const step = inner / days.length;
  const barW = Math.max(2, Math.min(38, step - 6));

  const svg = node('svg', {
    viewBox: `0 0 ${W} ${H}`,
    width: '100%',
    height: 'auto',
    role: 'img',
    'aria-label': `Подписки и отписки по дням, максимум ${max} за день`,
  });

  svg.append(node('line', {
    x1: padL, x2: W - 4, y1: zero, y2: zero,
    stroke: 'var(--line)', 'stroke-width': 1,
  }));

  const scale = (v: number, room: number) => (room * v) / max;
  const points: string[] = [];
  const sized = days.filter((d) => d.members > 0);
  const maxMembers = Math.max(1, ...sized.map((d) => d.members));

  days.forEach((d, i) => {
    const x = padL + i * step + (step - barW) / 2;

    const g = node('g', {});
    const title = node('title', {});
    title.textContent = `${human(d.date)}: +${d.joined}, −${d.left}` +
      (d.members ? `, в канале ${d.members}` : '');
    g.append(title);

    if (d.joined) {
      const h = Math.max(scale(d.joined, up), 2);
      g.append(node('rect', { x, y: zero - h, width: barW, height: h, rx: 3, fill: 'var(--accent)' }));
    }
    if (d.left) {
      const h = Math.max(scale(d.left, down), 2);
      g.append(node('rect', { x, y: zero, width: barW, height: h, rx: 3, fill: 'var(--bad, #c2410c)' }));
    }
    svg.append(g);

    // Линия размера идёт по тем дням, где снимок был. Дни без снимка не
    // достраиваются нулём: канал не схлопывался, мы просто не смотрели.
    if (d.members) {
      const y = 8 + (zero - 8) * (1 - d.members / maxMembers);
      points.push(`${x + barW / 2},${y}`);
    }

    const every = Math.ceil(days.length / 12);
    if (i % every === 0 || i === days.length - 1) {
      const t = node('text', {
        x: x + barW / 2, y: H - 6,
        fill: 'var(--muted)', 'font-size': 10, 'text-anchor': 'middle',
      });
      t.textContent = human(d.date);
      svg.append(t);
    }
  });

  if (points.length > 1) {
    svg.append(node('polyline', {
      points: points.join(' '),
      fill: 'none',
      stroke: 'var(--muted)',
      'stroke-width': 1.5,
      'stroke-dasharray': '4 3',
    }));
  }

  host.append(svg);
  host.append(
    el('p', { class: 'lede' },
      'Вверх — подписались, вниз — отписались. Пунктиром размер канала целиком, ',
      'включая тех, кого мы знаем только числом. Точные значения по наведению.'),
  );
}
