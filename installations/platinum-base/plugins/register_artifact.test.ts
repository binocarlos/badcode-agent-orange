import { describe, it, expect } from 'vitest';
import { registerArtifactPlugin } from './register_artifact.js';

describe('register_artifact plugin', () => {
  it('has the right name and sdkTool', () => {
    expect(registerArtifactPlugin.name).toBe('register_artifact');
    expect(registerArtifactPlugin.sdkTool).toBeTruthy();
  });
});
