// The four features of Home's security section, in the order they take their turns.

export interface Feature {
  /** The emblem drawn for it: see `EMBLEMS` in scripts/demos/emblems.ts. */
  key: 'source' | 'hosted' | 'builds' | 'trust';
  title: string;
  /** Shown only while the feature is the one picked. */
  text: string;
}

export const FEATURES: Feature[] = [
  {
    key: 'source',
    title: 'Open source',
    text: 'Every line that touches your mail is public. Read it, build it, and tell the world what you find.',
  },
  {
    key: 'hosted',
    title: 'Self-hosted',
    text: 'Run the same code in your own AWS account. Your servers, your keys, and we are out of the picture.',
  },
  {
    key: 'builds',
    title: 'Verifiable builds',
    text: 'Our servers prove which code they run. One command checks it against what we published.',
  },
  {
    key: 'trust',
    title: 'Zero trust',
    text: 'You don’t have to trust us. Your mail is opened only inside sealed hardware that runs the public code, and everything around it sees ciphertext.',
  },
];
