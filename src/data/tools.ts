import { SITE } from '../config';

const base = import.meta.env.BASE_URL.replace(/\/?$/, '/');

// Библиотека инструментов — хребет сайта. Единый источник: главная и /tools.
//
// Правило: сюда попадает только то, что уже работает на меня в проде.
// Ничего не строим ради сайта. Внутренности Viralmaxing не выкладываем —
// это IP компании. Здесь только личный тулинг.
//
// Порядок в массиве = порядок на главной. Дат нет намеренно: библиотека
// вечнозелёная, а не лента, поэтому пауза в пополнении не читается
// как заброшенность.
//
// href = страница-разбор; repo = open-source репозиторий; grab = что забирают.
// home: false — инструмент живёт только на /tools и не занимает место на главной.
export const tools = [
  {
    name: 'Starter-vault для здоровья',
    what: 'Apple Watch собирают факты, Obsidian хранит контекст, агент связывает всё в одну историю.',
    grab: 'Забрать vault',
    href: `${base}articles/digital-health`,
    icon: 'pulse' as const,
    home: false,
  },
  {
    name: 'Контент-агент',
    what: 'Автономный агент на Obsidian. Пишет посты твоим голосом — от разбора чужого видео до готового текста.',
    grab: 'Репозиторий и инструкция',
    href: `${base}tools/content-agent`,
    repo: SITE.hermesAgent,
    icon: 'doc-spark' as const,
    home: false,
  },
];

// Что показываем на главной: не всё подряд, а то, что тянет на витрину.
export const homeTools = tools.filter((t) => t.home !== false);
