export type NormalizedPDFLinkRegion = {
  x0: number;
  y0: number;
  x1: number;
  y1: number;
};

function normalizedRegion(startX: number, startY: number, endX: number, endY: number): NormalizedPDFLinkRegion {
  const clamp = (value: number) => Math.min(1, Math.max(0, value));
  return {
    x0: clamp(Math.min(startX, endX)),
    y0: clamp(Math.min(startY, endY)),
    x1: clamp(Math.max(startX, endX)),
    y1: clamp(Math.max(startY, endY)),
  };
}

export type PDFPoint = {
  x: number;
  y: number;
};

export type PDFLinkTarget = PDFPoint & {
  pageNumber: number;
  pageWidth: number;
  pageHeight: number;
};

export type PDFLinkPreview = {
  id: string;
  sourcePageNumber: number;
  sourceRegion: NormalizedPDFLinkRegion;
  target: PDFLinkTarget;
};

function sameTarget(left: PDFLinkPreview, right: PDFLinkPreview): boolean {
  return left.target.pageNumber === right.target.pageNumber &&
    Math.abs(left.target.x - right.target.x) <= 0.5 &&
    Math.abs(left.target.y - right.target.y) <= 0.5;
}

function adjacentOnSameLine(left: NormalizedPDFLinkRegion, right: NormalizedPDFLinkRegion): boolean {
  const overlap = Math.min(left.y1, right.y1) - Math.max(left.y0, right.y0);
  const minimumHeight = Math.min(left.y1 - left.y0, right.y1 - right.y0);
  if (minimumHeight <= 0 || overlap < minimumHeight * 0.5) return false;
  const horizontalGap = Math.max(0, Math.max(left.x0, right.x0) - Math.min(left.x1, right.x1));
  return horizontalGap <= 0.015;
}

// PDF generators often emit one Link annotation per word even though the
// words share one destination. Merge only neighboring rectangles on the same
// visual line; repeated references elsewhere on the page remain independent.
export function mergeAdjacentPDFLinks(links: PDFLinkPreview[]): PDFLinkPreview[] {
  const sorted = [...links].sort((left, right) =>
    left.sourcePageNumber - right.sourcePageNumber ||
    left.sourceRegion.y0 - right.sourceRegion.y0 ||
    left.sourceRegion.x0 - right.sourceRegion.x0,
  );
  const merged: PDFLinkPreview[] = [];
  for (const link of sorted) {
    const previous = merged.at(-1);
    if (previous && previous.sourcePageNumber === link.sourcePageNumber && sameTarget(previous, link) && adjacentOnSameLine(previous.sourceRegion, link.sourceRegion)) {
      previous.sourceRegion = {
        x0: Math.min(previous.sourceRegion.x0, link.sourceRegion.x0),
        y0: Math.min(previous.sourceRegion.y0, link.sourceRegion.y0),
        x1: Math.max(previous.sourceRegion.x1, link.sourceRegion.x1),
        y1: Math.max(previous.sourceRegion.y1, link.sourceRegion.y1),
      };
      continue;
    }
    merged.push({ ...link, sourceRegion: { ...link.sourceRegion } });
  }
  return merged;
}

function finiteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value);
}

// viewportRegion normalizes a rectangle after pdf.js PageViewport has applied
// the PDF page's CropBox, rotation, scale, and bottom-left-to-top-left
// transform. Doing that conversion in pdf.js is essential: hand-written
// y-flips are incorrect for named destinations and non-default page boxes.
export function viewportRegion(rect: unknown, pageWidth: number, pageHeight: number): NormalizedPDFLinkRegion | undefined {
  if (!Array.isArray(rect) || rect.length !== 4 || !rect.every(finiteNumber) || pageWidth <= 0 || pageHeight <= 0) return undefined;
  const [rawX0, rawY0, rawX1, rawY1] = rect;
  const x0 = Math.min(rawX0, rawX1) / pageWidth;
  const x1 = Math.max(rawX0, rawX1) / pageWidth;
  const y0 = Math.min(rawY0, rawY1) / pageHeight;
  const y1 = Math.max(rawY0, rawY1) / pageHeight;
  const region = normalizedRegion(x0, y0, x1, y1);
  if (region.x1 <= region.x0 || region.y1 <= region.y0) return undefined;
  return region;
}

function destinationStyle(value: unknown): string {
  if (typeof value === 'string') return value.toUpperCase();
  if (value && typeof value === 'object' && 'name' in value && typeof value.name === 'string') return value.name.toUpperCase();
  return '';
}

function destinationNumber(value: unknown): number | undefined {
  if (finiteNumber(value)) return value;
  if (value && typeof value === 'object' && 'num' in value && finiteNumber(value.num)) return value.num;
  return undefined;
}

// destinationPDFPoint extracts raw PDF coordinates. The caller must pass them
// through the target page's PageViewport.convertToViewportPoint; named and
// inline destinations share the same PDF coordinate system at this stage.
export function destinationPDFPoint(destination: unknown[]): PDFPoint | undefined {
  if (destination.length < 2) return undefined;
  const style = destinationStyle(destination[1]);
  const args = destination.slice(2);
  let x: number | undefined;
  let top: number | undefined;

  switch (style) {
  case 'XYZ':
    x = destinationNumber(args[0]);
    top = destinationNumber(args[1]);
    break;
  case 'FITH':
  case 'FITBH':
    x = 0;
    top = destinationNumber(args[0]);
    break;
  case 'FITR':
    x = destinationNumber(args[0]);
    top = destinationNumber(args[3]);
    break;
  default:
    return undefined;
  }

  if (x === undefined || top === undefined) return undefined;
  return { x, y: top };
}

// The destination is a point rather than a reference-entry rectangle. A
// bounded, nearly full-width strip gives enough original-PDF context without
// pretending that we know the exact citation boundary.
export function destinationPreviewRegion(target: PDFLinkTarget): NormalizedPDFLinkRegion {
  const horizontalMargin = Math.min(28, target.pageWidth * 0.045);
  const topPadding = 8;
  const previewHeight = Math.min(150, target.pageHeight * 0.2);
  return normalizedRegion(
    horizontalMargin / target.pageWidth,
    (target.y - topPadding) / target.pageHeight,
    (target.pageWidth - horizontalMargin) / target.pageWidth,
    (target.y - topPadding + previewHeight) / target.pageHeight,
  );
}
