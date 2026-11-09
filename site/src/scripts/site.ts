// The phone menu, the theme switch, and forms that have no backend yet.

const button = document.querySelector<HTMLElement>('.menu-btn');
const menu = document.querySelector<HTMLElement>('.menu');

button?.addEventListener('click', () => {
  if (!menu) return;
  const open = menu.dataset.open !== 'true';
  menu.dataset.open = String(open);
  button.setAttribute('aria-expanded', String(open));
  button.setAttribute('aria-label', open ? 'Close menu' : 'Menu');
  document.body.style.overflow = open ? 'hidden' : '';
});

const root = document.documentElement;
const themeButtons = [...document.querySelectorAll<HTMLElement>('.theme button')];

const paintTheme = () => {
  const current = root.dataset.theme ?? 'system';
  themeButtons.forEach((b) => b.setAttribute('aria-pressed', String(b.dataset.t === current)));
};

themeButtons.forEach((b) =>
  b.addEventListener('click', () => {
    try {
      if (b.dataset.t === 'system') {
        delete root.dataset.theme;
        localStorage.removeItem('permaily-theme');
      } else {
        root.dataset.theme = b.dataset.t;
        localStorage.setItem('permaily-theme', b.dataset.t!);
      }
    } catch {
      // The choice still applies to this page if storage is blocked.
      if (b.dataset.t !== 'system') root.dataset.theme = b.dataset.t;
    }
    paintTheme();
  }),
);
paintTheme();

document
  .querySelectorAll<HTMLFormElement>('form[data-no-backend]')
  .forEach((form) => form.addEventListener('submit', (event) => event.preventDefault()));
