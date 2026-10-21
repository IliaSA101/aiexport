// Точка входа: связывает статические поля index.html с состоянием и вешает кнопки.
// Обработчики — только через addEventListener (CSP default-src 'self' запрещает атрибуты on*).

import * as api from './api.js';
import { isFileExists } from './api.js';
import { state, update, register, markStale, buildSpec } from './state.js';
import { renderSources, addDraftSource } from './sources.js';
import { renderPicker, openPicker } from './picker.js';
import { renderResult, renderStatus } from './result.js';

const $ = (id) => document.getElementById(id);

// Синхронизирует статические поля и кнопки с состоянием. Значение пишем только если оно
// отличается, иначе курсор в поле прыгает во время набора.
function setValue(input, value) {
  if (input.value !== value) input.value = value;
}

function renderForm() {
  setValue($('draft-path'), state.draft.path);
  setValue($('draft-label'), state.draft.label);
  setValue($('draft-mode'), state.draft.mode);
  const busy = state.busy !== '';
  const noSources = state.sources.length === 0;
  const pathEmpty = state.draft.path.trim() === '';

  // Подсказка нужна рядом с кнопкой: человек, открывший программу впервые, иначе видит «мёртвые» кнопки.
  const addHint = state.draftError || (pathEmpty ? 'Чтобы добавить источник, укажите путь к папке или выберите её кнопкой «Обзор…».' : '');
  $('btn-add').disabled = pathEmpty;
  setHint('hint-add', 'btn-add', addHint, state.draftError !== '');

  // Путь уже набран, но не добавлен — самая частая причина «мёртвых» кнопок ниже.
  let runHint = '';
  if (busy) runHint = 'Дождитесь окончания текущей операции.';
  else if (noSources) runHint = pathEmpty
    ? 'Сначала добавьте хотя бы один источник в разделе «Источники».'
    : 'Путь введён, но не добавлен. Нажмите «Добавить» в разделе «Источники».';
  $('btn-plan').disabled = busy || noSources;
  $('btn-export').disabled = busy || noSources;
  setHint('hint-run', 'btn-plan', runHint, false);
  setHint(null, 'btn-export', runHint, false);
  $('btn-overwrite').disabled = busy;
  $('btn-overwrite-cancel').disabled = busy;
}

// Вопрос о замене файла живёт в самой странице: confirm() блокирует вкладку и выглядит чужеродно.
// Показываем его только по отказу сервера «файл существует» — никаких предварительных флажков.
function renderConfirm() {
  const box = $('confirm-overwrite');
  const pending = state.confirmOverwrite;
  box.hidden = !pending;
  $('confirm-detail').textContent = pending ? pending.message : '';
}

// Текст подсказки: видимый (если есть элемент) и в title кнопки. Пустой текст — подсказка исчезает.
function setHint(hintId, btnId, text, isError) {
  const btn = $(btnId);
  if (text) btn.setAttribute('title', text);
  else btn.removeAttribute('title');
  if (!hintId) return;
  const node = $(hintId);
  node.textContent = text;
  node.className = isError ? 'hint error' : 'hint';
  node.hidden = text === '';
}

register('sources', renderSources);
register('picker', renderPicker);
register('form', renderForm);
register('confirm', renderConfirm);
register('result', renderResult);
register('status', renderStatus);

// Поля параметров: любое изменение делает показанный план устаревшим.
function bindOption(id, event, apply) {
  $(id).addEventListener(event, (e) => update((s) => {
    apply(s, e.target);
    markStale(s);
  }, 'result', 'status', 'confirm'));
}

bindOption('opt-exclude', 'input', (s, t) => { s.exclude = t.value; });
bindOption('opt-gitignore', 'change', (s, t) => { s.useGitignore = t.checked; });
bindOption('opt-maxsize', 'input', (s, t) => { s.maxSizeKB = t.value; });
bindOption('opt-instructions', 'input', (s, t) => { s.instructions = t.value; });
// Путь вывода на план не влияет, но сброс «Сохранено: …» уместен — путь уже другой.
// Подтверждение относилось к прежнему пути, поэтому гасим его.
$('opt-out').addEventListener('input', (e) => update((s) => {
  s.out = e.target.value;
  s.exported = null;
  s.confirmOverwrite = null;
}, 'status', 'confirm'));

$('draft-path').addEventListener('input', (e) => update((s) => {
  s.draft.path = e.target.value;
  s.draftError = '';
}, 'form'));
$('draft-path').addEventListener('keydown', (e) => {
  if (e.key === 'Enter') addDraftSource();
});
$('draft-label').addEventListener('input', (e) => update((s) => { s.draft.label = e.target.value; }, 'form'));
$('draft-mode').addEventListener('change', (e) => update((s) => { s.draft.mode = e.target.value; }, 'form'));
$('btn-add').addEventListener('click', addDraftSource);
$('btn-browse').addEventListener('click', openPicker);

// Общая обвязка для запросов, которым нужен Spec: проверка, блокировка кнопок, показ ошибки.
// Для экспорта отказ «файл существует» превращается в вопрос, а не в ошибку.
async function run(kind, call, onDone) {
  const { spec, error } = buildSpec(state);
  if (error) {
    update((s) => { s.error = error; }, 'status');
    return;
  }
  update((s) => {
    s.busy = kind;
    s.error = '';
    s.exported = null;
    s.confirmOverwrite = null;
  }, 'form', 'status', 'confirm');
  try {
    const data = await call(spec);
    update((s) => onDone(s, data), 'result', 'status');
  } catch (e) {
    if (kind === 'export' && isFileExists(e)) {
      update((s) => { s.confirmOverwrite = { message: e.message }; }, 'confirm');
    } else {
      update((s) => { s.error = e.message; }, 'status');
    }
  } finally {
    update((s) => { s.busy = ''; }, 'form', 'status');
  }
}

$('btn-plan').addEventListener('click', () => run('plan', api.plan, (s, data) => {
  s.plan = data;
  s.planStale = false;
}));

// force=true уходит только из кнопки «Заменить», то есть после того, как человек увидел вопрос.
function runExport(force) {
  return run('export', (spec) => api.exportBundle(spec, state.out.trim(), force), (s, data) => {
    // Ответ без пути не прячем: пользователь должен знать, что файл не подтверждён.
    s.exported = { path: data?.path ?? data?.out ?? '', bytes: data?.bytes, tokens: data?.tokens };
  });
}

$('btn-export').addEventListener('click', () => runExport(false));
$('btn-overwrite').addEventListener('click', () => runExport(true));
$('btn-overwrite-cancel').addEventListener('click', () => update((s) => { s.confirmOverwrite = null; }, 'confirm'));

update(() => {});
