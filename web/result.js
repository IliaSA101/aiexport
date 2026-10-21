// Сводка плана: числа, зона и причины приходят с сервера готовыми.
// Здесь только раскладка по экрану и русские названия кодов причин.

import { el, clear, fmtNum, fmtBytes } from './dom.js';
import { state } from './state.js';

const REASONS = {
  gitignore: 'Правило .gitignore',
  default: 'Исключено по умолчанию (vendor, node_modules и т. п.)',
  glob: 'Глоб исключений',
  binary: 'Бинарный файл',
  'too-big': 'Больше лимита размера',
  'mode-tree': 'Источник в режиме «только дерево»',
  encoding: 'Файл не в UTF-8 (перекодировка не выполняется)',
  symlink: 'Символическая ссылка не разворачивается',
  'too-deep': 'Глубже предела обхода',
  unreadable: 'Не удалось прочитать',
};

const ZONES = {
  green: 'Зелёная зона',
  yellow: 'Жёлтая зона',
  red: 'Красная зона — бандл велик',
};

function reasonName(code) {
  return REASONS[code] ?? code ?? '—';
}

function sourceLabel(plan, idx) {
  return plan.sources?.[idx]?.label || `Источник ${idx + 1}`;
}

function stat(title, value) {
  return el('div', { class: 'stat' }, el('div', { class: 'stat-value', text: value }), el('div', { class: 'stat-title', text: title }));
}

function table(headers, rows) {
  const thead = el('thead', {}, el('tr', {}, ...headers.map((h) => el('th', { text: h }))));
  const tbody = el('tbody', {}, ...rows.map((cells) => el('tr', {}, ...cells.map((c) => el('td', { text: String(c) })))));
  return el('table', {}, thead, tbody);
}

// Файлы источников в режиме «только дерево» не пропущены: модель видит их в дереве.
// Поэтому их число показываем отдельной плиткой, а не в «Пропущено».
// Сервер считает их сам: totals.treeOnly не входит в totals.skipped, и записей mode-tree
// нет в списке skipped. Вычитать здесь ничего не надо — вычитание дало бы ноль дважды.
const TREE_REASON = 'mode-tree';

function treeOnlyCount(plan) {
  return plan.totals?.treeOnly ?? plan.skipByReason?.[TREE_REASON] ?? 0;
}

function trulySkipped(plan) {
  return plan.totals?.skipped ?? 0;
}

function summary(plan) {
  const t = plan.totals ?? {};
  const zone = t.zone ?? '';
  const treeOnly = treeOnlyCount(plan);
  return el(
    'div',
    { class: 'summary' },
    stat('Файлов в бандле', fmtNum(t.included)),
    stat('Токенов', fmtNum(t.tokens)),
    stat('Размер', fmtBytes(t.bytes)),
    stat('Пропущено', fmtNum(trulySkipped(plan))),
    treeOnly > 0 ? stat('Только деревом', fmtNum(treeOnly)) : null,
    el('div', { class: `zone zone-${zone}`, text: ZONES[zone] ?? zone }),
  );
}

// Ноль файлов без пояснения читается как поломка. Отличаем «всё в режиме дерева» от «пусто или исключено».
function emptyNotice(plan) {
  if ((plan.totals?.included ?? 0) > 0) return null;
  const onlyTree = treeOnlyCount(plan) > 0 && trulySkipped(plan) === 0;
  const text = onlyTree
    ? 'Содержимое файлов не включено: все источники в режиме «только дерево», в файл попадёт лишь структура папок.'
    : 'В файл ничего не попало: источник пуст или всё в нём исключено. Причины — в блоке «Что пропущено и почему» ниже.';
  return el('p', { class: 'warn', text });
}

function sourcesTable(plan) {
  if (!plan.sources?.length) return null;
  const rows = plan.sources.map((s) => [
    s.label,
    s.mode === 'tree' ? 'Только дерево' : 'Полностью',
    fmtNum(s.files),
    fmtNum(s.tokens),
  ]);
  return el('div', {}, el('h3', { text: 'По источникам' }), table(['Источник', 'Режим', 'Файлов', 'Токенов'], rows));
}

// Причины идут отдельной таблицей, чтобы видеть картину целиком, а не только первые 500 файлов.
function skippedBlock(plan) {
  const byReason = Object.entries(plan.skipByReason ?? {}).filter(([code]) => code !== TREE_REASON);
  if (byReason.length === 0) return null;
  const rows = byReason.sort((a, b) => b[1] - a[1]).map(([code, n]) => [reasonName(code), fmtNum(n)]);
  const block = el('div', {}, el('h3', { text: 'Что пропущено и почему' }), table(['Причина', 'Файлов'], rows));

  // Сервер убирает записи mode-tree из списка ещё до среза в 500, так что реально пропущенные
  // файлы они не вытесняют. Фильтр оставлен как перестраховка на случай старого или иного сервера.
  const skipped = (plan.skipped ?? []).filter((f) => f.reason !== TREE_REASON);
  if (skipped.length > 0) {
    const total = byReason.reduce((a, [, n]) => a + n, 0);
    const note = total > skipped.length ? ` (показаны первые ${fmtNum(skipped.length)} из ${fmtNum(total)})` : '';
    const details = el('details', {}, el('summary', { text: `Список пропущенных файлов${note}` }));
    details.append(
      table(
        ['Источник', 'Файл', 'Причина', 'Правило'],
        skipped.map((f) => [sourceLabel(plan, f.source), f.rel, reasonName(f.reason), f.rule || '']),
      ),
    );
    block.append(details);
  }
  return block;
}

// Включённых файлов могут быть тысячи, поэтому таблицу строим только при первом раскрытии.
function includedBlock(plan) {
  const files = plan.files ?? [];
  if (files.length === 0) return null;
  const details = el('details', {}, el('summary', { text: `Включённые файлы (${fmtNum(files.length)})` }));
  let built = false;
  details.addEventListener('toggle', () => {
    if (!details.open || built) return;
    built = true;
    details.append(
      table(
        ['Источник', 'Файл', 'Размер', 'Токенов'],
        files.map((f) => [sourceLabel(plan, f.source), f.rel, fmtBytes(f.size), fmtNum(f.tokens)]),
      ),
    );
  });
  return details;
}

export function renderResult() {
  const root = document.getElementById('result');
  clear(root);
  const plan = state.plan;
  if (!plan) {
    root.append(el('p', { class: 'muted', text: 'Нажмите «Посчитать», чтобы увидеть, что попадёт в файл.' }));
    return;
  }
  if (state.planStale) {
    root.append(el('p', { class: 'warn', text: 'Параметры изменились после подсчёта. Нажмите «Посчитать» ещё раз.' }));
  }
  const wrap = el('div', { class: state.planStale ? 'plan stale' : 'plan' });
  for (const part of [summary(plan), emptyNotice(plan), sourcesTable(plan), skippedBlock(plan), includedBlock(plan)]) {
    if (part) wrap.append(part);
  }
  root.append(wrap);
}

// Строка статуса: занятость, ошибка или путь сохранённого файла.
export function renderStatus() {
  const root = document.getElementById('status');
  clear(root);
  if (state.busy === 'plan') root.append(el('p', { class: 'muted', text: 'Считаю…' }));
  if (state.busy === 'export') root.append(el('p', { class: 'muted', text: 'Собираю файл…' }));
  if (state.error) root.append(el('p', { class: 'error', text: state.error }));
  if (state.exported) {
    const { path, bytes, tokens } = state.exported;
    root.append(
      el(
        'p',
        { class: 'ok' },
        path ? 'Сохранено: ' : 'Готово, но сервер не сообщил путь файла.',
        path ? el('code', { text: path }) : null,
      ),
    );
    // Размер и токены — то, ради чего файл собирали. Если сервер их не прислал, не выдумываем.
    if (Number.isFinite(bytes) && Number.isFinite(tokens)) {
      root.append(el('p', { class: 'ok', text: `Размер: ${fmtBytes(bytes)}, токенов: ${fmtNum(tokens)}` }));
      if (bytes === 0 && tokens === 0) {
        root.append(el('p', { class: 'warn', text: 'Файл пуст: источник пуст или всё в нём исключено. Нажмите «Посчитать» — там видно, что и почему пропущено.' }));
      }
    }
  }
}
