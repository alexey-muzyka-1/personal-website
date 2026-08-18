import { SITE } from '../config';

// Метки источников для бота личной воронки.
//
// Один экран — одна метка, иначе непонятно, какое место действительно
// приводит людей. Те же строки лежат в bot/internal/funnel/catalog.go:
// по ним бот выбирает, что предложить пришедшему.
export const FUNNEL_SOURCE = {
  home: 'site_home',
  method6x5: 'site_metod6x5',
  blueprint50: 'site_blueprint50',
  health: 'site_health',
} as const;

export type FunnelSource = (typeof FUNNEL_SOURCE)[keyof typeof FUNNEL_SOURCE];

/**
 * Ссылка в бота с меткой источника.
 *
 * Пока юзернейм не вписан в config.ts — возвращает null, и ни одна
 * ссылка на бота не рендерится: ссылка на незапущенное это антипруф.
 * Сайт при этом работает ровно как раньше.
 */
export function botHref(source: FunnelSource): string | null {
  const bot = SITE.telegramBot.trim().replace(/^@/, '');
  if (!bot) return null;
  return `https://t.me/${bot}?start=${source}`;
}
