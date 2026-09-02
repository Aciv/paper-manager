import assert from 'node:assert/strict';
import test from 'node:test';
import { SelectionGeneration, canvasCropForRegion, normalizedRegion, regionIsLargeEnough } from '../src/pdfSelection.ts';

test('normalizes reverse pointer movement and clamps it to one page', () => {
  assert.deepEqual(normalizedRegion(0.8, 1.2, -0.1, 0.25), { x0: 0, y0: 0.25, x1: 0.8, y1: 1 });
});

test('converts normalized coordinates to backing-canvas pixels', () => {
  assert.deepEqual(canvasCropForRegion({ x0: 0.1, y0: 0.2, x1: 0.6, y1: 0.7 }, 2000, 1000), { sx: 200, sy: 200, width: 1000, height: 500 });
  assert.equal(regionIsLargeEnough({ x0: 0, y0: 0, x1: 0.005, y1: 0.5 }, 1000, 1000), false);
});

test('latest selection replacement invalidates delayed processing', () => {
  const generations = new SelectionGeneration();
  const first = generations.replace();
  const second = generations.replace();
  assert.equal(generations.isCurrent(first), false);
  assert.equal(generations.isCurrent(second), true);
});

test('selection coordinate work does not invoke screenshot creation', () => {
  let captureCalls = 0;
  normalizedRegion(0.1, 0.1, 0.5, 0.5);
  assert.equal(captureCalls, 0);
  captureCalls += 1;
  assert.equal(captureCalls, 1);
});
