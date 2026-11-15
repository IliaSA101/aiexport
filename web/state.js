// Всё состояние интерфейса живёт в одном объекте. Любое изменение идёт через update(),
// который перерисовывает экран из этого объекта: так UI не расходится с тем,
// что на самом деле уйдёт на сервер.

export const state = {
  sources: [], // { path, label, mode }
  draft: { path: '', label: '', mode: 'full' },
  draftError: '', // ошибка проверки пути при «Добавить»; гаснет при правке пути
  exclude: '',
  useGitignore: true,
  maxSizeKB: '',
  instructions: '',
  out: '',
  plan: null, // последний PlanResponse как есть
  planStale: false, // параметры изменились после «Посчитать»
  exported: null, // { path, bytes, tokens } после «Собрать»
  busy: '', // '' | 'plan' | 'export'
  error: '',
  confirmOverwrite: null, // { message } — сервер отказал: файл уже существует, ждём решения человека
  picker: { open: false, loading: false, path: '', parent: '', dirs: [], drives: [], error: '' },
};

const renderers = new Map();

export function register(name, fn) {
  renderers.set(name, fn);
}

// update(мутатор, ...части). Без частей перерисовывается всё; с частями — только они.
// Частичная перерисовка нужна, чтобы набор текста в полях не терял фокус.
export function update(mutator, ...parts) {
  mutator(state);
  const names = parts.length ? parts : [...renderers.keys()];
  for (const n of names) renderers.get(n)?.();
}

// Меняется что-то, влияющее на результат: план и сохранённый путь устарели.
export function markStale(s) {
  if (s.plan) s.planStale = true;
  s.exported = null;
  s.confirmOverwrite = null; // вопрос относился к прежним параметрам
}

// Собирает Spec для API. Возвращает { spec } или { error }.
export function buildSpec(s) {
  if (s.sources.length === 0) return { error: 'Добавьте хотя бы один источник.' };
  const raw = s.maxSizeKB.trim();
  let maxFileSize = 0; // 0 — сервер подставит значение по умолчанию
  if (raw !== '') {
    const kb = Number(raw.replace(',', '.'));
    if (!Number.isFinite(kb) || kb < 0) return { error: 'Лимит размера — неотрицательное число в килобайтах.' };
    maxFileSize = Math.round(kb * 1024);
  }
  return {
    spec: {
      sources: s.sources.map((x) => ({ path: x.path, label: x.label, mode: x.mode })),
      exclude: s.exclude.split('\n').map((l) => l.trim()).filter((l) => l !== ''),
      useGitignore: s.useGitignore,
      maxFileSize,
      instructions: s.instructions,
    },
  };
}
