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
