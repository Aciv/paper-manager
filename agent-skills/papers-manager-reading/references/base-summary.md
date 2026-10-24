# Base summary workflow

Status: host-side workflow. No dedicated `base_summary` MCP tool exists yet;
use the existing read tools and assemble the result in the host. The result is
a standalone HTML report, not a new Base entity and not a reading note.

## Inputs

- Base ID and, if needed, a bounded member subset.
- The user's comparison question or output format.
- A budget for member count, section reads, and exact evidence checks.

## Retrieval ladder

First define the comparison question and dimensions. A broad request may use
task, mechanism, evidence and deployment conditions as defaults; state the scope.
Use `base_list_documents` for actual membership and `library_get_many` for bounded
metadata batches rather than calling library_get once per member.

1. **L0 — membership and metadata**
   - list Base members;
   - read title, authors, year, venue, DOI, tags, abstract;
   - record missing notes and missing source outlines.

2. **L1 — compact structure**
   - read note outline/description and source-outline label for each member;
   - select candidate papers and candidate sections;
   - do not read every note body.

3. **L2 — selected notes/sections**
   - read notes first; they already contain analysis and evidence;
   - read source sections only where the note is missing or insufficient;
   - compare methods, assumptions, datasets, results, and limitations.

4. **L3 — verification**
   - for each material comparison claim, read the exact page/Block;
   - record the verification state and a source hint for the report;
   - do not use raw PDFs unless the structured path cannot answer.

## Output: one self-contained HTML report

Before ranking results, check task/input/output alignment, dataset and split,
evaluation protocol, metric unit/direction, training data, model size, external
tools and compute budget. Distinguish direct controlled comparisons from indirect
cross-paper observations. If conditions differ, explain trade-offs instead of
ranking raw numbers. Preserve these conditions as normal prose or table cells,
not a new schema.

## Style selection

Before writing the report, ask which visual style to use unless the user already
specified one:

- `neutral` — default, balanced;
- `editorial` — magazine / long-form;
- `swiss` — Swiss grid / formal comparison;
- `brutalist` — neo-brutalist / high-energy;
- `cyberpunk` — neon HUD / deliberately flashy;
- or a **custom style**: one sentence from the user is enough. Do not ask for a
  template, placeholder contract, or additional style specification.

If the user has no preference or says “默认 / 随便 / you choose”, use `neutral`.
Do not ask style questions repeatedly or per section.

For a built-in style, choose exactly one template from
[html_template/](html_template/README.md); the default is
[neutral.html](html_template/neutral.html). For a custom style, build the
one-off report directly from the user's sentence. Do not mix a custom style
with a built-in template. Keep the result a single HTML file. The report is a
navigation and synthesis layer; do not copy whole notes or source sections into
it.

The template's sections (`核心结论`, `对比`, `分论文`, `证据缺口`, `成员状态`) are
only a presentation example, not a required information architecture. Rename,
merge, split, reorder, or omit them as the comparison demands; do not
mechanically reproduce the sample headings. The report should still expose the
following information somewhere useful:

- scope: Base name and UUID, generation time, member count, selected/skipped
  counts, Base link, and the snapshot warning;
- a direct answer to the comparison question, not a list of every paper;
- a condition-aware comparison of methods/results/assumptions, in a matrix or
  another layout that fits the question;
- per-paper coverage: note availability, related note sections, main findings,
  and evidence gaps;
- explicit gaps: skipped members, missing notes/outlines, unread material, and
  budget exhaustion;
- member status: one entry per Base member with note/outline availability.

Do not hide a skipped member in a footnote. State why it was skipped.

## Report path and persistence

Write the report under the current workspace by default:

```text
reports/base-summary/<base_id>-<YYYYMMDD-HHMMSS>.html
```

- Create `reports/base-summary/` if needed. `reports/` is ignored by the project
  repository.
- Never overwrite an existing report. If the generated name collides, append
  `-2`, `-3`, and so on after checking the directory.
- Use the Base UUID in the filename; do not use the Base title as a path
  component.
- Tell the user the absolute path (or workspace-relative path plus the workspace
  root) in the final answer. Do not auto-open the browser unless the user asks.
- If the user requests a different filename or location, use that location.
- Do not persist the summary as a reading note, Base description, or database
  record. A future explicit Base-note design is still out of scope.
- If the host has no file-write capability, say so. Provide the HTML only when
  the user asks for it; do not use shell HTTP clients or scripts to work around
  the MCP boundary.

## Link policy: document level only

The report links to the reading library, not to an in-app note section. The
current reader opens the selected document with the right panel on 论文信息 and
does not select a note section from a URL. Do not invent a section URL or claim
that a link opens a specific note section.

Use these fixed destinations:

```text
Base:  http://localhost:5173/#/library?base=<base_id>
Paper: http://localhost:5173/#/library?doc=<document_id>
```

Rules:

- Use the document UUID returned by the API; never use a title, filename, DOI,
  or array index as the `doc` value.
- Link text should be human-readable and identify the relevant note section,
  for example `《Attention Is All You Need》· 方法`. Do not use “点击这里” or a bare
  URL as link text.
- The section name in the link text is a locator hint only. The link opens the
  paper; the user may still need to switch to 阅读注释 and choose the section.
- Put a link next to each material claim, or group tightly related claims under
  one link. Do not collect all links in a separate table at the end.
- If a paper has no reading note, link the paper and mark `无阅读笔记`; do not
  name a section that does not exist.
- If a document ID is missing, mark the item as missing and do not emit a broken
  `href`.
- Open links with `target="_blank"` and `rel="noopener noreferrer"` so the
  report remains open.

The `papers-manager://.../evidence?...` link format is for reading-note Markdown
and the in-app preview. Do not put it in this standalone HTML report and expect
it to navigate. Keep the source page/section hint as visible text, for example
`原文 §3，p.5`, and record whether the claim was verified.

### Template fragments

The template's placeholders expect HTML fragments, not escaped JSON. Replace
`DOC_ID` and `BASE_ID` with real UUIDs and escape every API-derived label.

Comparison header cell:

```html
<th scope="col"><a href="http://localhost:5173/#/library?doc=DOC_ID" target="_blank" rel="noopener noreferrer">《论文短标题》</a></th>
```

Comparison row:

```html
<tr>
  <th scope="row">方法</th>
  <td>两阶段训练；先对齐再微调。<br>
    <a href="http://localhost:5173/#/library?doc=DOC_ID" target="_blank" rel="noopener noreferrer">《论文短标题》· 方法</a>
  </td>
</tr>
```

Paper card:

```html
<article class="paper-card">
  <h3><a href="http://localhost:5173/#/library?doc=DOC_ID" target="_blank" rel="noopener noreferrer">《论文短标题》</a></h3>
  <p class="paper-meta">作者 · 2025 · 会议/期刊</p>
  <p><span class="status status-ok">已建笔记</span> <span class="status status-ok">关键结论已核验</span></p>
  <ul class="note-list">
    <li><a href="http://localhost:5173/#/library?doc=DOC_ID" target="_blank" rel="noopener noreferrer">《论文短标题》· 方法</a> — 两阶段训练的主要机制与适用条件。</li>
    <li><a href="http://localhost:5173/#/library?doc=DOC_ID" target="_blank" rel="noopener noreferrer">《论文短标题》· 结果</a> — 报告的主结果与对照条件。</li>
  </ul>
</article>
```

Evidence gap item:

```html
<li><strong>《论文短标题》</strong> <span class="status status-gap">证据缺口</span>
  当前笔记没有给出这个比较所需的训练数据规模；原文对应部分未读取。</li>
```

Member status item:

```html
<li><a href="http://localhost:5173/#/library?doc=DOC_ID" target="_blank" rel="noopener noreferrer">《论文短标题》</a>
  <span class="status status-ok">已建笔记</span>
  <span class="status status-muted">未参与比较：与问题无关</span></li>
```

Use `status-ok` for available/verified states, `status-warn` for missing or
unverified states, `status-gap` for a material evidence gap, and `status-muted`
for skipped/not-applicable items. The badge text must carry the meaning without
color.

## HTML safety and rendering

Start from the chosen template and keep the output self-contained:

- escape every API-derived value for its HTML context (`&`, `<`, `>`, `"`, and
  `'` in attributes); this includes titles, authors, venue names, note titles,
  and extracted prose;
- do not insert raw Markdown or raw HTML returned by a read tool; convert it to
  the small HTML subset you need;
- use only text, headings, paragraphs, lists, tables, blockquotes, emphasis,
  inline code, and ordinary links; do not add scripts. The built-in
  `cyberpunk.html` already contains one fixed inline interaction script for
  randomized animations; keep it unchanged and do not add other scripts;
- do not load remote CSS, fonts, images, or analytics; use the template's inline
  style block;
- do not put untrusted text in `href`; paper and Base URLs are constructed from
  UUIDs only;
- keep the layout readable at narrow widths and in print; the template already
  handles both.

## Report content rules

- Separate what the paper reports, what an existing note concludes, and what
  this comparison infers. Do not present an inference as a paper finding.
- A document link proves only that the target exists; it does not prove the
  claim. Keep the verification state honest and report gaps.
- Keep exact numbers and protocol conditions; do not normalize incompatible
  metrics into a single ranking.
- Prefer concise prose and a compact comparison matrix over repeating the same
  conclusion in every paper card.
- State the reading budget and any unread sections when they limit the answer.

## Budgets

- Select a bounded candidate set before reading bodies.
- Prefer notes to source sections and sections to full documents.
- Stop expanding when the user's question is answered.
- Report exactly what was not read and why.

## Future tool

A future `base_summary` tool may package this workflow, but its result must
still expose:

- the same report contents and link policy;
- per-member status and missing inputs;
- evidence states and gaps;
- total budgets and partial results;
- no hidden full-document reads.

Until then, do not claim that a `base_summary` tool exists.
