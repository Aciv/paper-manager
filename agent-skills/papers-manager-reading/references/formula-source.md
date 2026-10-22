# Formula source

## Decision

papersManager currently treats the **source PDF/block index** as the authoritative
formula source. It does not run a formula OCR model and must not reconstruct
LaTeX from garbled PDF text.

This means:

- the note can explain what a formula means;
- the formula itself remains in the PDF;
- the note points to the exact page/rectangle or Block;
- the UI opens the PDF and highlights the target.

Inline mathematical typesetting is a separate future feature and requires a
validated LaTeX source.

## Formula reference format

Prefer a Block hint when available:

```markdown
[公式 (1) — p.4](papers-manager://self/evidence?v=1&page=4&block=BLOCK_UUID)
```

Fall back to a normalized page rectangle:

```markdown
[公式 (1) — p.4](papers-manager://self/evidence?v=1&page=4&rect=0.12,0.35,0.86,0.48)
```

Rules:

- `page` is mandatory.
- Use `block` only when the Block belongs to the current recognition generation.
- `rect` is normalized to the page (`0..1`) and may repeat for a multi-line
  formula.
- The link label should identify the formula number and page when known.
- Explain variables, assumptions, and consequences in ordinary prose; do not
  copy the formula's garbled text layer.
- If no reliable location exists, write an explicit formula gap; never invent a
  page, Block, or LaTeX expression.

## Reading existing Markdown

The current parser output may contain Unicode or HTML-like fragments:

```text
divide each by<sup>_√_</sup> _dk_
```

Treat these as a rendering artifact, not as formula truth. Do not translate such
fragments into LaTeX and claim the conversion is exact. Locate the source region
instead.

## Future LaTeX source

If a future parser provides a verified LaTeX/MathML source, the note may include
it using standard math delimiters (`$...$` for inline, `$$...$$` for display),
but only after checking that the fragment is valid and belongs to the current
source generation. The page/Block evidence link should remain for verification.
