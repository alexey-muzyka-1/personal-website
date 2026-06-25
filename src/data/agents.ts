import { SITE } from '../config';

// Открытые агенты. Единый источник — главная и /agents.
// href = страница-гайд на сайте; repo = open-source репозиторий.
export const agents = [
  {
    name: 'Контент-агент',
    what: 'Автономный контент-агент на Obsidian. Пишет посты твоим голосом.',
    href: `${import.meta.env.BASE_URL.replace(/\/?$/, '/')}agents/content-agent`,
    repo: SITE.hermesAgent,
  },
];
