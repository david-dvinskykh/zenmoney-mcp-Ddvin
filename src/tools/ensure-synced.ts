import type { ZenState } from "../state.js";

type ToolResult = {
  content: { type: "text"; text: string }[];
  isError: true;
};

/**
 * Sync on demand so callers never have to run sync_data first. Returns null
 * when data is ready, or a tool error result describing why it is not.
 */
export async function ensureSynced(
  state: ZenState
): Promise<ToolResult | null> {
  try {
    await state.ensureSynced();
    return null;
  } catch (error) {
    return {
      content: [
        {
          type: "text" as const,
          text: `Automatic sync failed: ${error instanceof Error ? error.message : String(error)}`,
        },
      ],
      isError: true,
    };
  }
}
