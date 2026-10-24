# Base summary HTML templates

These files are standalone report shells for the `papers-manager-reading` Base
summary workflow. They share one placeholder contract and one content policy,
but use different visual systems. Each is an original interpretation of a
current web/report design direction — not a clone of a specific brand, and not
a copy of its assets, fonts, or layouts.

The former single `base-summary-template.html` is now `neutral.html`.

## Style index

| File | Style | Use when |
| --- | --- | --- |
| `neutral.html` | Balanced default | No style was requested; the safest general-purpose report |
| `editorial.html` | Magazine / newspaper | Narrative comparison, long-form reading, strong typographic hierarchy |
| `swiss.html` | Swiss / international typographic | Formal, structured comparison where order, contrast and grid matter |
| `brutalist.html` | Neo-brutalist | High-energy, creative or indie context that needs a strong personality |
| `cyberpunk.html` | Cyberpunk / neon HUD | Deliberately flashy, technical or demo context; diagonal split hero, scroll-focus reveal, polygonal borders, palette modes, random interaction FX and tactile tags |

The skill asks which style to use before writing a report, unless the user has
already specified one. Default to `neutral.html`. If the user requests a mood or
the content clearly fits one of the other directions, choose exactly one style.
Do not combine templates or copy a fragment from one style into another.

If the user describes a custom style, one sentence is enough: build a one-off
report from that direction. No template or placeholder contract is required. If
it becomes reusable, promote it into this folder and update this index.

## Consolidation

An earlier pass also had `bento`, `dark`, `notion`, and `aurora` files. They were
removed because they changed mostly the palette, surface treatment, or section
stack while keeping the same reading structure:

- `dark`, `bento`, and `aurora` were essentially theme/layout skins over the
  same stacked report;
- `notion` repeated editorial's side-directory long-read structure with a warmer
  palette.

Keep only templates that change how the report is read. If a dark or bento
surface is needed later, implement it as a variant of one of the remaining
templates rather than duplicating the entire content contract into another file.

The section headings in the templates (`核心结论`, `对比`, `分论文`,
`证据缺口`, `成员状态`) are a display scaffold, not a required information
architecture. Rename, merge, split, reorder, or omit them as the comparison
requires. Keep the content requirements, link policy, persistence path and HTML
safety rules from [../base-summary.md](../base-summary.md); this README only
covers style.

## Shared placeholder contract

All templates use the same placeholders, so a finished report can be moved
between styles without rewriting its content:

| Placeholder | Expected content |
| --- | --- |
| `{{REPORT_TITLE}}` | Plain text for `<title>`; escape it |
| `{{BASE_NAME}}` | Base display name |
| `{{SCOPE_SUMMARY}}` | One-paragraph scope and comparison question |
| `{{BASE_URL}}` | `http://localhost:5173/#/library?base=<base_id>` |
| `{{BASE_ID}}` | Base UUID |
| `{{GENERATED_AT}}` | Generation timestamp with timezone |
| `{{MEMBER_COUNT}}` | Total Base members |
| `{{SELECTED_COUNT}}` | Members included in the comparison |
| `{{SKIPPED_COUNT}}` | Members excluded from the comparison |
| `{{TAKEAWAYS}}` | HTML fragment: short answer and key conclusions |
| `{{COMPARISON_CAPTION}}` | Table caption text |
| `{{COMPARISON_HEADER_CELLS}}` | Repeated `<th scope="col">...</th>` cells |
| `{{COMPARISON_ROWS}}` | Repeated `<tr>...</tr>` comparison rows |
| `{{PAPER_CARDS}}` | Repeated `<article class="paper-card">...</article>` blocks |
| `{{EVIDENCE_GAPS}}` | Repeated `<li>...</li>` gap items |
| `{{MEMBER_STATUS_ROWS}}` | Repeated `<li>...</li>` member status items |

Placeholders expect HTML fragments, not Markdown, JSON, or escaped JSON. Escape
every API-derived value for its HTML context (`&`, `<`, `>`, `"`, and `'` in
attributes), and leave no `{{...}}` markers in the saved report.

The shared link and status conventions are:

- paper links: `http://localhost:5173/#/library?doc=<document_id>`;
- Base links: `http://localhost:5173/#/library?base=<base_id>`;
- link text includes the paper title and, when a note is available, the
  relevant note section, for example `《论文短标题》· 方法`;
- paper links open the paper, not a specific note section; say so if the user
  expects an automatic section jump;
- `papers-manager://` evidence links belong in reading-note Markdown and the
  in-app preview, not in a standalone HTML file;
- `status-ok`, `status-warn`, `status-gap`, and `status-muted` badges must keep
  their visible text; color alone is not the label.

Keep each report self-contained. Most templates use inline CSS only; the
built-in `cyberpunk.html` also includes one fixed inline interaction script for
randomized hover/focus/press animations and background color flashes. That
script never reads report content, makes network requests, or loads external
code. Do not add any other scripts, remote stylesheets, fonts, images, or
analytics. Every template has responsive and print styles; preserve them. All
templates switch to a light print layout.

## Style notes

- **neutral** — Soft gray surfaces, restrained blue accent, clear cards. The
  default when no presentation preference is given.
- **editorial** — Warm paper tone, serif display type, drop cap, rules, and a
  sticky side directory. Good for a narrative comparison rather than a KPI
  dashboard.
- **swiss** — Oversized grotesque title, strict black rules, numbered sections,
  and a single red accent. Best when the report is a formal comparison matrix.
- **brutalist** — Thick black outlines, hard offset shadows, saturated color
  blocks, and chunky type. Use for personality, not for formal corporate or
  accessibility-critical material without review.
- **cyberpunk** — A diagonal split hero: the left side carries the neon
  console label and Base title; the right side is a vertical Base metadata
  readout. Scrolling pulls the diagonal halves apart and brings the content into
  focus. Panels, cards, metadata boxes, tags and controls use clipped polygonal
  boundaries that swap shape on hover/press; section headers and TOC items use
  triangle/circle markers. The bright animated color storm, `NEON` / `CHROME` /
  `ACID` palettes and `CHAOS` motion switch sit behind the content. A fixed
  inline script picks a random animation for every interactive element on
  pointer-over, focus and press and flashes the background through a random
  hue. Use for a deliberately flashy technical report; keep body copy clean and
  respect reduced-motion preferences.

## Research basis

The styles are informed by current web and report-design directions rather than
a single template. The recurring signals across the references were:

- editorial/magazine layouts with serif/sans mixing, strong grids and print-like
  pacing;
- Swiss/international typography and 2025 “bruta-minimalism” with oversized
  grotesque type, asymmetric grids and one strong accent;
- bento grids and dark product-first interfaces, which were widespread in
  2024–2026 for product and dashboard surfaces (Apple, Linear, Vercel); these
  informed the default report's card/module treatment rather than becoming
  separate templates;
- warm minimalism and documentation-like reading surfaces (Notion), which
  informed the editorial reading-column treatment;
- neo-brutalism with thick outlines, hard shadows and saturated blocks
  (Gumroad, Figma marketing, `neobrutalism.dev`) and cyberpunk/Y2K HUD
  aesthetics with neon grids, scanlines, glitch and holographic/chrome accents;
- iridescent mesh gradients and restrained glass, associated with Stripe;
- digital-first report design: KPI/big-number modules, editorial storytelling,
  responsive tables, accessibility and print adaptivity.

For a Base summary, these are presentation choices only. They do not change the
comparison method, reading budgets, or evidence requirements.

### Sources consulted

- [The Plus Addons — Web Design Trends 2026](https://theplusaddons.com/blog/web-design-trends-2026/)
- [Bubble — Web Design Trends 2026](https://bubble.io/blog/web-design-trends/)
- [Awwwards — 网页设计中的流行渐变](https://www.awwwards.com/wang-ye-she-ji-zhong-de-liu-xing-jian-bian.html)
- [Visual Best — Annual Report Design Trends 2026](https://www.visualbest.co/blogs/annual-report-design-trends-2026-adopt-adapt-avoid/)
- [Popular web design systems catalog](https://skillsauth.com/skills/nousresearch/popular-web-designs)
- [Neobrutalism.dev](https://neobrutalism.dev/)
- [Arcade Hub — Glowing Gradients: Cyberpunk Aesthetics in Web Design](https://arcadehubplay.com/blog/glowing-gradients-cyberpunk-web-design)
- [Skills UI — Cyberpunk design system](https://www.skillsui.app/skills/cyberpunk)
- [Haddington & Haddington — Web design trends 2025](https://www.haddingtoncreative.com/post/top-web-design-trends-to-look-out-for-in-2025)
- [Y2K Brand Web Design 2026 — Chrome, Glitter, Lime Green](https://digitalheroesco.com/styles/y2k-nostalgia/)
- Example sites referenced by those sources: [Notion](https://www.notion.so/),
  [Linear](https://linear.app/), [Vercel](https://vercel.com/),
  [Stripe](https://stripe.com/), [Gumroad](https://gumroad.com/),
  [Figma](https://www.figma.com/), [The Verge](https://www.theverge.com/),
  [It's Nice That](https://www.itsnicethat.com/).

## Maintenance

When adding a style:

1. Start from the shared placeholder contract and, where useful, the neutral
   body; change the reading layout when the new style needs it.
2. Keep the content slots, link rules, escaping rules and status labels
   unchanged.
3. Keep the report self-contained and include responsive and print rules.
4. Update the style index and notes above.
5. Render with sample content at desktop and narrow widths, check for horizontal
   overflow, and confirm that every placeholder is replaced.
