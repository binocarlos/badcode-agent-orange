import { describe, it, expect } from 'vitest';
import { generatePptxPlugin } from './generate_pptx.js';

describe('generate_pptx plugin', () => {
  it('has the right name and sdkTool', () => {
    expect(generatePptxPlugin.name).toBe('generate_pptx');
    expect(generatePptxPlugin.sdkTool).toBeTruthy();
  });
});
