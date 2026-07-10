import { Args, Command } from '@oclif/core';
import { runRoutingMode } from '../../../lib/claude/runSettings.js';
import type { RoutingMode } from '../../../lib/claude/settings-apply.js';

export default class ClaudeSettingsMode extends Command {
  static description =
    'Select the cache-aware routing mode: off (per-request, default), sticky (hysteresis: stay on the conversation model unless a switch beats the prompt-cache cost), smartsqueeze (sticky + compact the context on a switch so the new model reprocesses a small prefix), or orchestrator (shadow-mode v2, computed not served).';

  static args = {
    mode: Args.string({
      description: 'off, sticky, smartsqueeze, or orchestrator',
      options: ['off', 'sticky', 'smartsqueeze', 'orchestrator'],
      required: true,
    }),
  };

  static examples = [
    '<%= config.bin %> claude settings mode sticky',
    '<%= config.bin %> claude settings mode smartsqueeze',
    '<%= config.bin %> claude settings mode off',
  ];

  async run(): Promise<void> {
    const { args } = await this.parse(ClaudeSettingsMode);
    await runRoutingMode(args.mode as RoutingMode, (code) => this.exit(code));
  }
}
