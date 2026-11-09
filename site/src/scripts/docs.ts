// The theme switch in the footer on docs pages. It stores the same choice as the website.
// Unlike the website, it always sets data-theme to light or dark, because Starlight's styles and code blocks read it.

const KEY = 'permaily-theme';

const root = document.documentElement;
const system = matchMedia('(prefers-color-scheme: dark)');
const buttons = [...document.querySelectorAll<HTMLElement>('.theme button')];

const initial = (): string => {
  try {
    return (
      new URLSearchParams(location.search).get('theme') ?? localStorage.getItem(KEY) ?? 'system'
    );
  } catch {
    return 'system';
  }
};

let choice = initial();

const paint = () => {
  root.dataset.theme =
    choice === 'light' || choice === 'dark' ? choice : system.matches ? 'dark' : 'light';
  buttons.forEach((button) =>
    button.setAttribute('aria-pressed', String(button.dataset.t === choice)),
  );
};

buttons.forEach((button) =>
  button.addEventListener('click', () => {
    choice = button.dataset.t ?? 'system';
    try {
      if (choice === 'system') localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, choice);
    } catch {
      // The choice still applies to this page if storage is blocked.
    }
    paint();
  }),
);

system.addEventListener('change', paint);
paint();

export {};
