// Навигация админки в два уровня.
//
// Плоский ряд из пяти вкладок врал про устройство: «Канал» стоял рядом с
// «Источниками» и «Сообщениями», как будто это такой же отчёт по боту.
// Это другая система — свой источник данных, свои ограничения (списка
// подписчиков не существует) и свои вопросы.
//
// Разделов три, и они отвечают на разные вопросы:
//   Бот    — довели ли человека от метки до предложения
//   Канал  — остался ли он рядом после этого
//   Люди   — с кем именно и что делать дальше
//
// «Люди» не подраздел бота специально: карточка человека сводит обе
// стороны, и решение «кому написать» принимается там, а не в отчёте.

export interface NavPage {
  id: string;
  label: string;
  path: string;
}

export interface NavSection {
  id: string;
  label: string;
  /** hint — что за вопрос закрывает раздел. Виден под названием. */
  hint: string;
  pages: NavPage[];
}

export const sections: NavSection[] = [
  {
    id: 'bot',
    label: 'Бот',
    hint: 'от метки до предложения',
    pages: [
      { id: 'funnel', label: 'Обзор', path: '' },
      { id: 'sources', label: 'Источники', path: 'sources/' },
      { id: 'scenario', label: 'Сообщения', path: 'scenario/' },
    ],
  },
  {
    id: 'channel',
    label: 'Канал',
    hint: 'кто остался рядом',
    pages: [{ id: 'channel', label: 'Канал', path: 'channel/' }],
  },
  {
    id: 'people',
    label: 'Люди',
    hint: 'с кем и что делать',
    pages: [{ id: 'people', label: 'Люди', path: 'people/' }],
  },
];

/** sectionOf — какому разделу принадлежит страница. Карточка человека
 *  живёт в «Людях»: открывают её оттуда и возвращаются туда же. */
export function sectionOf(pageId: string): NavSection {
  return sections.find((s) => s.pages.some((p) => p.id === pageId)) ?? sections[0];
}
