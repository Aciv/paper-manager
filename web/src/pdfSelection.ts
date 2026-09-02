export type NormalizedPDFRegion = {
  x0: number;
  y0: number;
  x1: number;
  y1: number;
};

type SelectionBase = { documentId: string };

export type LatestPDFSelection =
  | (SelectionBase & { kind: 'text'; text: string; pageNumber?: number; region?: NormalizedPDFRegion })
  | (SelectionBase & { kind: 'block'; blockId: string; pageNumber?: number; blockType: string; previewText: string })
  | (SelectionBase & { kind: 'area'; pageNumber: number; region: NormalizedPDFRegion });

export type PixelCrop = { sx: number; sy: number; width: number; height: number };

export function normalizedRegion(startX: number, startY: number, endX: number, endY: number): NormalizedPDFRegion {
  const clamp = (value: number) => Math.min(1, Math.max(0, value));
  return { x0: clamp(Math.min(startX, endX)), y0: clamp(Math.min(startY, endY)), x1: clamp(Math.max(startX, endX)), y1: clamp(Math.max(startY, endY)) };
}

export function regionIsLargeEnough(region: NormalizedPDFRegion, displayedWidth: number, displayedHeight: number, minimumPixels = 8): boolean {
  return (region.x1 - region.x0) * displayedWidth >= minimumPixels && (region.y1 - region.y0) * displayedHeight >= minimumPixels;
}

export function canvasCropForRegion(region: NormalizedPDFRegion, canvasWidth: number, canvasHeight: number): PixelCrop {
  const sx = Math.max(0, Math.floor(region.x0 * canvasWidth));
  const sy = Math.max(0, Math.floor(region.y0 * canvasHeight));
  const x1 = Math.min(canvasWidth, Math.ceil(region.x1 * canvasWidth));
  const y1 = Math.min(canvasHeight, Math.ceil(region.y1 * canvasHeight));
  return { sx, sy, width: Math.max(1, x1 - sx), height: Math.max(1, y1 - sy) };
}

// SelectionGeneration invalidates delayed results after selection replacement.
export class SelectionGeneration {
  private value = 0;
  replace(): number { this.value += 1; return this.value; }
  current(): number { return this.value; }
  isCurrent(generation: number): boolean { return generation === this.value; }
}
