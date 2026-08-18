// Единая точка для ссылок и аналитики.
// Меняешь здесь — меняется на всём сайте. Ничего не теряется по файлам.
export const SITE = {
  telegram: 'https://t.me/+4XwhB8jbNFJhMmUy', // открытый чат
  telegramDM: 'https://t.me/Lesha_Muzyka', // личка для консультаций
  hermesAgent: 'https://github.com/alexey-muzyka-1/hermes-content-agent', // open-source контент-агент
  hermesDocs: 'https://hermes-agent.nousresearch.com/docs/',

  // Бот личной воронки: юзернейм без @, например 'leshamuzyka_bot'.
  // Пока пусто — на сайте нет ни одной ссылки на бота и всё работает как
  // раньше. Впишешь — блок на главной и концовки статей начнут вести в
  // бота, каждое место со своей меткой источника (src/data/funnel.ts).
  telegramBot: '',

  // Канал. Пока пусто — блок на главной ведёт в чат (SITE.telegram).
  // Впишешь ссылку — блок начнёт вести в канал.
  // Впишешь число подписчиков — оно покажется рядом как соцпруф.
  // Это единственная цифра, которую договорились показывать.
  telegramChannel: '',
  telegramSubscribers: 0,

  // UTM нужен, чтобы Google Analytics НА СТОРОНЕ Viralmaxing видел,
  // что переход пришёл с этого сайта. Source можешь поменять на свой домен.
  viralmaxing: 'https://viralmaxing.com/?utm_source=lesha-site&utm_medium=referral',

  // App Store не читает utm_*, Apple их выкидывает. Хочешь однажды понять,
  // сколько установок пришло с сайта — сгенерируй campaign link в App Store
  // Connect (App Analytics → Acquisition → Campaigns) и вставь его сюда целиком.
  // Пока трафика мало, обычной ссылки достаточно.
  viralmaxingAppStore: 'https://apps.apple.com/us/app/viralmaxing-viral-scripts/id6784994172',

  // Google Analytics: впиши Measurement ID (вид G-XXXXXXXXXX), когда заведёшь.
  // Пусто = аналитика не подключается.
  gaId: 'G-P0E0PKP89T',
};
