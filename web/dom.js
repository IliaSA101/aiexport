// Мелкие помощники для сборки DOM. Весь текст идёт только через textContent
// и createTextNode: имена файлов и пути приходят из чужой файловой системы
// и считаются недоверенным вводом.

// el('button', {class: 'x', text: 'Привет', attrs: {type: 'button'}, on: {click: fn}}, ...дети)
export function el(tag, opts = {}, ...children) {
  const node = document.createElement(tag);
  if (opts.class) node.className = opts.class;
  if (opts.text !== undefined) node.textContent = opts.text;
  if (opts.attrs) {
    for (const [k, v] of Object.entries(opts.attrs)) node.setAttribute(k, v);
  }
  if (opts.on) {
    for (const [ev, fn] of Object.entries(opts.on)) node.addEventListener(ev, fn);
  }
  for (const child of children) {
    if (child === null || child === undefined || child === false) continue;
    node.append(typeof child === 'string' ? document.createTextNode(child) : child);
  }
  return node;
}

export function clear(node) {
  node.replaceChildren();
}

const nf = new Intl.NumberFormat('ru-RU');

export function fmtNum(n) {
  return nf.format(n ?? 0);
}

// Размер показываем человеку; сами байты считает сервер.
export function fmtBytes(n) {
  const b = n ?? 0;
  if (b < 1024) return `${fmtNum(b)} Б`;
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(1).replace('.', ',')} КБ`;
  return `${(b / 1024 / 1024).toFixed(1).replace('.', ',')} МБ`;
}
