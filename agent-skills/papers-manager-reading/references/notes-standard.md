# Reading-note standard

This is the default output standard for `papers-manager-reading`. It is a
content and presentation contract for the Agent; the backend still permits any
valid H1-H3 section Markdown.

## Modes

- **brief**: only when the user explicitly asks for a short note. Keep 4-5
  sections and focus on the thesis, method, main result, and one limitation.
- **standard**: the default for a normal paper. Use 6-8 sections.
- **deep**: use when the user asks for a full analysis or the paper is central to
  the task. Split setup, results, ablations, and evidence gaps into separate
  sections.

## Standard template

```markdown
# 论文概览

**论文**：标题、作者、venue、DOI（有则写）
**阅读范围**：Abstract、§3、§4.2、pp.4–8
**一句话结论**：用 1-2 句说明论文解决了什么，结论是什么。
**关键贡献**
- ……
- ……

# 问题与动机

说明问题、已有方法的限制，以及论文为什么需要新的方案。

# 方法与关键设计

**核心思路**
……

**关键设计**
- ……

**公式引用**
- [公式 (1) — p.4](papers-manager://self/evidence?v=1&page=4&rect=0.12,0.35,0.86,0.48)
  说明公式表达的关系、变量含义和设计作用；公式本体保留在 PDF。

# 实验设置与结果

**设置**
- 数据集、baseline、metric、训练/推理配置

**主要结果**
- ……

**消融与分析**
- ……

# 局限与风险

- 作者承认的限制
- 你认为存在的威胁、未覆盖场景或证据不足
- 需要区分的“论文没有证明”与“结果错误”

# 开放问题

- ……

# 证据缺口

> 证据缺口：……
```

For a deep note, split “实验设置与结果” into `# 实验设置`, `# 结果与分析`,
and `# 消融与失败案例`.

## Section content requirements

- **论文概览** must contain a one-sentence conclusion, reading scope, and
  contributions; do not merely restate the abstract.
- **问题与动机** explains why prior approaches are insufficient.
- **方法与关键设计** explains mechanisms, inputs/outputs, and design choices;
  formula references are links, not guessed LaTeX.
- **实验设置与结果** includes datasets, baselines, metrics, concrete numbers,
  and important ablations.
- **局限与风险** distinguishes acknowledged limitations from the reviewer's
  own concerns.
- **开放问题** contains actionable follow-ups, not rhetorical filler.
- Every important claim should carry an evidence link or be listed in
  **证据缺口**.

## Presentation rules

The current web reader supports headings, paragraphs, bullet/numbered lists,
blockquotes, fenced code, inline code, bold/italic, and links. It does not yet
render pipe tables, raw HTML, or LaTeX. Until those renderers exist:

- use short paragraphs (typically 2-5 lines);
- use `**标签**：内容` lead-ins for dense facts;
- use bullets for contributions, setup, results, and limitations;
- do not generate pipe tables or raw HTML;
- do not generate `$...$`/`$$...$$` formulas; link the source region instead;
- do not use images in the note body;
- keep one section focused on one question.

These are constraints on generated content, not a new database schema.

## Quality checklist

- Does the overview state the paper's thesis, not just its topic?
- Are the contributions concrete and differentiated from prior work?
- Does the method explain how it works, including key design choices?
- Are experiment settings and numbers specific enough to check?
- Are limitations and open questions genuinely about this paper?
- Are claims evidence-linked or explicitly marked as gaps?
- Is the note analysis rather than copied or paraphrased paper prose?
- Would a reader know what to read next in the PDF?
