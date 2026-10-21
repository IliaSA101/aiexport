// Обзор файловой системы через GET /api/browse. Что показывать и куда можно ходить,
// решает сервер (он отвергает UNC и прочее); здесь только отображение его ответа.

import { el, clear } from './dom.js';
import * as api from './api.js';
import { state, update } from './state.js';

let seq = 0; // номер последнего запроса: запоздавший ответ не должен затирать свежий

function sepOf(path) {
  return path.includes('\\') || /^[A-Za-z]:/.test(path) ? '\\' : '/';
}

// Сервер может вернуть либо имена подпапок, либо полные пути. Если есть разделитель —
// считаем путь полным, иначе приклеиваем к текущему.
function fullPath(base, name) {
  if (/[\\/]/.test(name)) return name;
  const sep = sepOf(base);
  return base.endsWith(sep) ? base + name : base + sep + name;
}

export async function loadDir(path) {
  const my = ++seq;
  update((s) => {
    s.picker.open = true;
    s.picker.loading = true;
    s.picker.error = '';
  }, 'picker');
  try {
    const r = await api.browse(path);
    if (my !== seq) return;
    update((s) => {
      s.picker = {
        open: true,
        loading: false,
        path: r.path ?? '',
        parent: r.parent ?? '',
        dirs: r.dirs ?? [],
        drives: r.drives ?? [],
        error: '',
      };
    }, 'picker');
  } catch (e) {
    if (my !== seq) return;
    update((s) => {
      s.picker.loading = false;
      s.picker.error = e.message;
    }, 'picker');
  }
}

export function openPicker() {
  return loadDir(state.draft.path.trim());
}

function closePicker() {
  seq++;
  update((s) => {
    s.picker.open = false;
  }, 'picker');
}

function choose() {
  update((s) => {
    s.draft.path = s.picker.path;
    s.picker.open = false;
  }, 'picker', 'form');
}

function entry(label, path) {
  return el('li', {}, el('button', {
    class: 'dir secondary',
    text: label,
    attrs: { type: 'button', title: path },
    on: { click: () => loadDir(path) },
  }));
}

export function renderPicker() {
  const root = document.getElementById('picker');
  const p = state.picker;
  root.hidden = !p.open;
  clear(root);
  if (!p.open) return;

  const atDrives = p.path === '';
  root.append(
    el(
      'div',
      { class: 'picker-head' },
      el('strong', { text: atDrives ? 'Выберите диск или папку' : p.path, class: 'picker-path' }),
      el('button', { class: 'secondary', text: 'Закрыть', attrs: { type: 'button' }, on: { click: closePicker } }),
    ),
    el(
      'div',
      { class: 'picker-bar' },
      el('button', {
        class: 'secondary',
        text: '↑ Вверх',
        attrs: { type: 'button', ...(atDrives || p.loading ? { disabled: '' } : {}) },
        on: { click: () => loadDir(p.parent) },
      }),
      el('button', {
        text: 'Выбрать эту папку',
        attrs: { type: 'button', ...(atDrives || p.loading ? { disabled: '' } : {}) },
        on: { click: choose },
      }),
    ),
  );

  if (p.error) root.append(el('p', { class: 'error', text: p.error }));
  if (p.loading) root.append(el('p', { class: 'muted', text: 'Загружаю…' }));

  const ul = el('ul', { class: 'dirs' });
  for (const d of p.drives) ul.append(entry(d, d));
  for (const d of p.dirs) ul.append(entry(d.split(/[\\/]/).filter(Boolean).pop() ?? d, fullPath(p.path, d)));
  if (!p.loading && !p.error && ul.children.length === 0) {
    root.append(el('p', { class: 'muted', text: 'Вложенных папок нет.' }));
  }
  root.append(ul);
}
