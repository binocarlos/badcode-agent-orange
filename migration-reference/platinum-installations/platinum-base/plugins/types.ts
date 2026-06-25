export interface MarkerSpec {
  key: string;
  event: string;
  toEvent(payload: any): Record<string, unknown>;
  toModelText(payload: any): string;
}
export interface ToolPlugin {
  name: string;
  sdkTool: any;
  marker?: MarkerSpec;
}
