# 0032 Drawings made of sample mail on the public site

Status: Accepted, built, not released.
Date: 2026-11-14

## Context

The site's design is strict on purpose: two colours with fixed meanings, hairlines, and motion limited to two wipes and counting numbers ([design.md](../design.md), [site.md](../site.md)). The maintainer asked for something artistic beside it, to contrast with that strictness and to earn trust through beauty, and for Home to carry fewer animations and fewer repeated numbers. Five things constrain how:

1. **The colour law.** Black means withheld and the highlight means shared, and nothing else uses either.
2. **The site's rules for animations.** Each one is a pure function of time, the HTML holds its result, and reduced motion shows that result.
3. **The CSP.** No outside origin, so no hosted image, video or library.
4. **WCAG 2.2 AA.** Motion that goes on for more than five seconds needs a way to pause it.
5. **The page's own words.** Sample mail must not become text that a search engine indexes or a screen reader reads out.

## Options

All six were sketched live with the same subject, the logo's iceberg, so that only the treatment changed.

- **A living painting** in its own colours, drawn by a shader. The warmest. It needs an exception to the colour law, and a canvas has no result in the HTML.
- **Dither and dots** in ink, paper and the highlight. It keeps the colour law. Also a canvas.
- **Light**: glow on a dark ground. Close to the neon look of security products that the design avoids, and it only works on dark.
- **The drawn line**: pen, highlighter and marker. Honest and quiet, better for diagrams than for a hero.
- **Classical paintings** set against the product. It needs outside artwork.
- **Text as image**: the picture is built from lines of sample mail in the mono face, readable on the highlight where shared and covered by bars where withheld.

## Decision

Text as image, chosen by the maintainer from the sketches.

- **Two drawings, both on Home.** The hero: sample mail drifts past the logo's iceberg, readable on the highlight in the tip and covered by bars in the mass. How it works: mail drifts through three stretches, "Your inbox", "Permaily" and "Other apps"; inside Permaily, what an app asked for takes the highlight and goes on to the apps, some of it with one detail covered, and a bar wipes over each word of the rest until all of it is covered, and it stops there.
- **They replace two animations.** The inbox animation leaves the hero, and the three-column flow diagram leaves How it works. The inbox animation moves to Companies, merged with the vendors animation: a visitor picks a vendor and flips a Permaily switch.
- **HTML and CSS, not canvas.** Rows of text are moved with transforms and shown through windows cut with `clip-path`. The HTML holds the result, both themes come from the tokens, and there is no new dependency.
- **The browser moves the rows, and the script only cuts the windows.** The first build had the existing engine set every row's transform on every frame. That kept a slow phone's main thread busy for most of each second, so the maintainer asked for the drawings to be as cheap as they can be on such a device without losing quality (15 November 2026). Each row is now a move with no end that the browser runs by itself, a row has a move only while its drawing moves, a settled drawing looks 20 times a second whether a character has crossed an edge, and a drawing out of the window is not drawn. Nothing that is seen in motion changed. [site.md](../site.md), "What the drawings cost a slow phone", has the rules, the reasons and the numbers.
- **The hero is built from whole characters, as the chosen sketch was.** The first build cut the logo's outline straight through the text, and the maintainer preferred the sketch, where the outline steps along the letters. So a character belongs to the tip, the mass or the sea by where its middle is: the script works out each row's window from the logo's geometry and moves its edges only when a character crosses one. Without the script the two shapes are cut to the logo's outline, which is the same picture with a smooth edge.
- **Every covered word is one bar, set in a font.** A covered row is written as x's with its spaces kept and set in a font of two blocks, each as wide as a character of the mono face (`site/scripts/bars-font.py` makes it). That needs no element for each word, the mail is never in a covered row, and a browser without the font shows x's. Two kinds of bar are elements of their own, set in the same font: the one over a detail of an email that goes on to the apps, and the ones the pointer leaves on the words of the hero's sea, which last a moment. The blocks are drawn in whole pixels for the sizes the drawings use, so their edges stay sharp.
- **The mail is drawn from attributes.** Each row's mail sits in `data-` attributes and is drawn with CSS generated content, inside layers marked `aria-hidden`. The figure's label says what the drawing shows.
- **Drift is ambient motion.** It is a move with no end, so the engine gains one mode: it goes on after the opening, pauses when the drawing is scrolled away, and each drawing has a Pause control. Reduced motion shows the still result. In that mode a drawing can also say that nothing it draws will change for a while, and the engine leaves the page alone until then.
- **No example numbers.** The headline and the labels of both drawings name no figure and no app: "Stop giving away your inbox", "Your inbox", "Permaily", "Other apps".
- **A third drawing joined them**: the emblem of Home's security section, in place of the terminal that typed a command. Its first form, a shield with a still picture for each of four features, lasted a day. The maintainer then asked that the words stay as they are when the feature changes, for a symbol of its own for each feature, and for a smaller figure with nothing drawn around it, and chose from a board of sketches the one where one outline melts into the next (15 November 2026). So one field of mail drifts, as in the hero, and an emblem is cut out of it from whole characters: code brackets, a rack of servers, a seal with a check mark, a padlock. The words of the features are the maintainer's: open source, self-hosted, verifiable builds, zero trust. [site.md](../site.md) says how it is built, and what is open about the first of those words.
- **The sample grew to 72 emails**, so that no email shows twice in How it works, and what passes there is the mail services send by themselves: codes, receipts, orders, bookings, what customers write back. A detail that is covered belongs to its email, so it is covered wherever the email shows.

## Consequences

- The motion rule widens from three moves to five, with drift and, since the emblem, one outline that melts into another. [site.md](../site.md) says so.
- A stopped drawing is drawn with the page and not as layers of its own, so its text can sit one device pixel away from where it is while it moves.
- The site gains a tool that is run by hand, `site/scripts/drawings-cost.mjs`, which measures what the drawings cost with the processor slowed down.
- The colour law holds inside the drawings. No exception was needed.
- Home's HTML is about 138 KB, 20 KB compressed. Most of it is the rows of the drawings. With 72 emails in place of 20 the rows repeat less, so they compress less.
- The site gains one font file of its own, 348 bytes, which the build puts inside the stylesheet, and the script that makes it, which needs Python's fontTools and is run by hand.
- `site/public/og.png` is a capture of the hero, so it was captured again.
- The sample data changes: `site/src/data/mail.ts` holds the lines of the drawings, and `site/src/data/vendors.ts` holds a sample company inbox beside the vendors.

## Not verified

- **A real slow phone.** The cost was measured in headless Chrome on a laptop, at a phone's size and with the processor slowed six times: the screen held 60 frames a second, and the page's main thread worked for about a quarter of each second in the hero and less in the other two. No real device was tried, and no browser but Chrome, where the same rules may weigh differently.
- **Real playback.** The stills at chosen moments, frames captured while playing, the engine's tests and both themes were checked. Nobody watched the drawings play at full speed on a phone.
- **The hero on a phone.** It is the same drawing, smaller, with fewer characters across the tip.
- **Screen readers.** Checked with axe only, in Chromium.
