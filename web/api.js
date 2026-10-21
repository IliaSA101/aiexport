// Тонкая обёртка над HTTP API. Никакой логики подсчёта: что вернул сервер, то и отдаём.

// Ошибка ответа сервера. status и code нужны вызывающему коду, чтобы отличать ситуации
// (например, «файл уже существует») не по тексту сообщения: тексты меняются.
export class ApiError extends Error {
  constructor(message, status, code) {
    super(message);
    this.status = status;
    this.code = code ?? '';
  }
}

// Отказ записи, потому что out уже существует, а force не выставлен.
// Сервер должен отвечать HTTP 409 и/или полем code: "file-exists" в JSON ошибки.
export function isFileExists(e) {
  return e instanceof ApiError && (e.code === 'file-exists' || e.status === 409);
}

async function request(method, url, body) {
  const init = { method, headers: {} };
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  let resp;
  try {
    resp = await fetch(url, init);
  } catch {
    throw new Error('Нет связи с сервером aiexport. Он ещё запущен?');
  }
  const text = await resp.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null; // ответ не JSON — для ошибок ниже покажем сырой текст
  }
  if (!resp.ok) {
    const msg = (data && (data.error || data.message)) || text.trim() || `Ошибка сервера: HTTP ${resp.status}`;
    throw new ApiError(msg, resp.status, data?.code);
  }
  return data;
}

export function plan(spec) {
  return request('POST', '/api/plan', spec);
}

// download:false — сервер сам пишет файл на диск и возвращает путь, а не отдаёт тело.
// force уходит только как осознанное подтверждение замены; по умолчанию поля в запросе нет.
export function exportBundle(spec, out, force = false) {
  const body = { ...spec, out, download: false };
  if (force) body.force = true;
  return request('POST', '/api/export', body);
}

export function browse(path) {
  return request('GET', `/api/browse?path=${encodeURIComponent(path)}`);
}
