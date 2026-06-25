// Barrel: the default export is the flat array the generic loader registers.
import renderTables from './render_table.js';
import { renderChartPlugin } from './render_chart.js';
import { createDashboardPlugin } from './create_dashboard.js';
import { generatePptxPlugin } from './generate_pptx.js';
import { registerArtifactPlugin } from './register_artifact.js';
import { hoistSkillPlugin } from './hoist_skill.js';
import { searchSkillsPlugin } from './search_skills.js';
import { installSkillPlugin } from './install_skill.js';
import type { ToolPlugin } from './types.js';

const plugins: ToolPlugin[] = [
  ...renderTables, // render_table + render_tables
  renderChartPlugin,
  createDashboardPlugin,
  generatePptxPlugin,
  registerArtifactPlugin,
  hoistSkillPlugin,
  searchSkillsPlugin,
  installSkillPlugin,
];

export default plugins;
