# Note generation

## Structure policy

Use the template and mode rules in [notes-standard.md](notes-standard.md).
Default to the standard analysis-first structure:

```markdown
# 论文概览
# 问题与动机
# 方法与关键设计
# 实验设置与结果
# 局限与风险
# 开放问题
# 证据缺口
```

For a brief note, merge setup/results and omit the separate gap section only when
the user explicitly asks for brevity. For a deep note, split method, setup,
results, and ablations.

Do not copy the paper's complete outline into note headings. The source outline
already describes the paper and can change after re-recognition; the note is a
separate analysis artifact.

For a long paper or an explicit user request, support an optional walkthrough:

```markdown
# 逐节笔记
## Introduction
## Method
## Experiments
## Limitations
```

Keep it bounded to major source sections, not every heading. Record the source
mapping in each section description.

Always preserve an existing note's structure during a targeted update. Do not
reorder, rename, or rewrite unrelated sections. When the user asks to expand or
complete an existing note, add the missing analysis sections and enrich the
existing ones instead of treating the current short structure as final.

## Depth standard

A complete reading note is analysis, not a shortened abstract. Unless the user
explicitly asks for a brief note, cover:

- the problem and why it matters;
- the paper's contributions and how they differ from prior approaches;
- the method: components, equations/mechanisms, inputs/outputs, and design
  choices;
- training/evaluation setup: datasets, baselines, hyperparameters, hardware, or
  other reproducibility facts;
- quantitative results and ablations, including important comparisons;
- limitations, assumptions, open questions, or inconsistencies.

Use specific facts, numbers, mechanisms, and comparisons. A section with one
generic paragraph is usually too shallow for a full note. Keep sections bounded
and readable; do not compensate for vagueness by copying paper text.

Key claims should carry evidence links when the source location is available.
If the note cannot locate a claim, write an explicit evidence gap rather than
inventing precision.

## Rendering constraints

The current web reader supports headings, paragraphs, bullet/numbered lists,
blockquotes, fenced code, inline code, bold/italic, and links. It does not render
pipe tables, raw HTML, or inline LaTeX yet. Generated notes should therefore:

- use short paragraphs and bullets;
- use `**标签**：内容` lead-ins for dense facts;
- link formulas by page/Block/rectangle instead of guessing LaTeX;
- avoid pipe tables, raw HTML, and note-body images until supported;
- keep one section focused on one question.

## Section shape

Each note section is one H1-H3 heading plus body content. The heading level is
part of the section identity. Body edits must not introduce another H1-H3
heading.

Write with semantic edits:

- `addSection` for a new section;
- `replaceSection` for a complete section replacement;
- `replaceRange` / `replaceText` / `insertAt` / `appendText` for body changes.

Edit fields are op-specific: `replaceText` uses `old_text` + `new_text`;
`replaceRange`, `insertAt`, `appendText`, `replaceSection`, and `addSection`
use `content`. Do not put append/insert text in `new_text`.

Do not simulate structure changes by typing headings into the body.

## Section metadata

An optional description can be placed immediately after the heading:

```markdown
# 方法

<!-- papers-manager:section-meta v=1
{"description":"对应原文 §3，pp.4–6；概括模型结构和训练目标。"}
-->

正文……
```

Rules:

- version 1 currently supports only `description`;
- description is one paragraph, at most 512 Unicode code points;
- it is navigation metadata, not evidence and not a copy of the body;
- it is stored inside section Markdown; the backend does not parse it;
- consumers parse it, strip it from previews, and may derive an outline;
- do not fabricate a description when the section has not been read.

Useful description content:

- the source section/page range;
- what question this section answers;
- what is intentionally out of scope.

Bad description content:

- the full section body;
- unsupported claims;
- a list of evidence links that belong in the body.

## Formula references

Do not treat the current Markdown's formula text as LaTeX. Prefer a link to the
source formula location over a guessed transcription:

```markdown
- [公式 (1) — p.4](papers-manager://self/evidence?v=1&page=4&rect=0.12,0.35,0.86,0.48)
  缩放点积注意力；说明变量、缩放原因和它在模型中的作用。
```

Use a Block UUID when available; otherwise use a normalized page rectangle. If
the formula cannot be located, write an explicit formula gap. See
[formula-source.md](formula-source.md).

## Where source references go

Place references at three levels:

1. **Claim level**: a `papers-manager://` evidence link next to the claim.
2. **Section level**: a short source mapping in `section-meta.description`.
3. **Note/paper level**: cite the paper title/DOI once in the summary.

Do not create a separate evidence table or bibliography dump in the note.

If a claim cannot be located in the source, keep the claim only when it is
useful as an open question, and mark it:

```markdown
> 证据缺口：论文没有给出可定位的页码或 Block。
```

## Create vs update

Creating a note:

- only when the user asks, or when an explicit generation request is confirmed;
- use `papers_manager_reading_note_create` with `expected_revision=0`;
- write a bounded first version, not the entire paper.

Updating a note:

- read the manifest first and capture `expected_revision`;
- read only the target section(s);
- apply edits with the captured revision;
- 409 means the note changed: reload and re-plan, never retry blindly;
- verify the returned revision and changed section IDs.

## Example

```markdown
# 方法

<!-- papers-manager:section-meta v=1
{"description":"对应原文 §3，pp.4–6；说明模型结构和训练目标。"}
-->

模型由编码器和解码器组成，训练目标是最小化重构误差。
[第 5 页](papers-manager://self/evidence?v=1&page=5&rect=0.12,0.25,0.86,0.48)

作者没有报告训练数据的去重方式。

> 证据缺口：数据预处理细节在当前 section 中没有可定位位置。
```
