// Список источников: подпись, режим и порядок правятся прямо в строке.

import { el, clear } from './dom.js';
import { state, update, markStale } from './state.js';

function move(i, delta) {
  update((s) => {
    const j = i + delta;
    if (j < 0 || j >= s.sources.length) return;
    [s.sources[i], s.sources[j]] = [s.sources[j], s.sources[i]];
    markStale(s);
  }, 'sources', 'result', 'status');
}

function remove(i) {
  update((s) => {
    s.sources.splice(i, 1);
    markStale(s);
  }, 'sources', 'form', 'result', 'status');
}

function iconButton(text, label, onClick, disabled) {
  return el('button', {
    class: 'icon secondary',
    text,
    attrs: { type: 'button', 'aria-label': label, title: label, ...(disabled ? { disabled: '' } : {}) },
    on: { click: onClick },
  });
}

function row(src, i, total) {
  const labelInput = el('input', {
    class: 'src-label',
    attrs: { type: 'text', value: src.label, 'aria-label': 'Подпись источника', autocomplete: 'off' },
    on: {
      // Только 'result': полная перерисовка списка отняла бы фокус у поля во время набора.
      input: (e) => update((s) => {
        s.sources[i].label = e.target.value;
        markStale(s);
      }, 'result', 'status'),
    },
  });

  const mode = el(
    'select',
    {
      class: 'src-mode',
      attrs: { 'aria-label': 'Режим источника' },
      on: {
        change: (e) => update((s) => {
          s.sources[i].mode = e.target.value;
          markStale(s);
        }, 'result', 'status'),
      },
    },
    el('option', { text: 'Полностью', attrs: { value: 'full' } }),
    el('option', { text: 'Только дерево', attrs: { value: 'tree' } }),
  );
  mode.value = src.mode;

  return el(
    'li',
    { class: 'src-row' },
    el('span', { class: 'src-num', text: String(i + 1) }),
    el('span', { class: 'src-path', text: src.path, attrs: { title: src.path } }),
    labelInput,
    mode,
    el(
      'span',
      { class: 'src-btns' },
      iconButton('↑', 'Выше', () => move(i, -1), i === 0),
      iconButton('↓', 'Ниже', () => move(i, 1), i === total - 1),
      iconButton('✕', 'Удалить источник', () => remove(i), false),
    ),
  );
}

export function renderSources() {
  const root = document.getElementById('sources-list');
  clear(root);
  if (state.sources.length === 0) {
    root.append(el('p', { class: 'empty', text: 'Источников пока нет. Добавьте хотя бы один ниже.' }));
    return;
  }
  const ol = el('ol', { class: 'sources' });
  state.sources.forEach((src, i) => ol.append(row(src, i, state.sources.length)));
  root.append(ol);
}

// Последний сегмент пути как подпись по умолчанию: пустая подпись в заголовках секций
// бесполезна, а заставлять вводить её руками для каждой папки утомительно.
function baseName(path) {
  const parts = path.split(/[\\/]+/).filter((p) => p !== '');
  return parts.length ? parts[parts.length - 1] : path;
}

// Только очевидное: пустой путь и сетевой (UNC) путь. Остальное — существует ли папка,
// доступна ли она — знает только сервер, его проверку здесь не дублируем.
export function draftPathError(path) {
  if (path === '') return 'Укажите путь к папке.';
  if (path.startsWith('\\\\') || path.startsWith('//')) {
    return 'Сетевые пути (\\\\сервер\\ресурс) не поддерживаются. Подключите ресурс как диск или укажите локальный путь.';
  }
  return '';
}

export function addDraftSource() {
  update((s) => {
    const path = s.draft.path.trim();
    const err = draftPathError(path);
    if (err) {
      s.draftError = err;
      return;
    }
    s.sources.push({
      path,
      label: s.draft.label.trim() || baseName(path),
      mode: s.draft.mode,
    });
    // Режим тоже сбрасываем в «Полностью»: иначе «только дерево» залипает
    // и следующий источник незаметно уходит деревом.
    s.draft = { path: '', label: '', mode: 'full' };
    s.draftError = '';
    s.error = '';
    markStale(s);
  });
}
