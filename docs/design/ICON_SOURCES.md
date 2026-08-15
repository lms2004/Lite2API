# Lite2API provider icon sources

The admin UI embeds provider marks as inline SVG symbols so the single-file web UI
does not depend on a runtime CDN. The marks identify the upstream service next to
account, model, authentication, and adapter labels; they are not Lite2API branding.

## Provider mapping

| UI provider | Embedded symbol | Source |
| --- | --- | --- |
| OpenAI / Codex | `b-openai` | OpenAI Blossom from the official OpenAI brand resources |
| Anthropic / Claude | `b-anthropic` | Simple Icons v16 `anthropic.svg` |
| Google Gemini | `b-gemini` | Simple Icons v16 `googlegemini.svg` |
| DeepSeek | `b-deepseek` | Simple Icons v16 `deepseek.svg` |
| xAI / Grok | `b-x` | Simple Icons v16 `x.svg` |
| Kimi | `i-moon` | Original neutral moon glyph; no third-party brand asset embedded |
| CLIProxy / local adapters | `i-gateway` / `i-atom` | Original Lite2API interface glyphs |

Sources were retrieved on 2026-08-14. Brand marks remain the property of their
respective owners and should only be shown when directly identifying that provider.

- OpenAI brand guidelines: <https://openai.com/brand/>
- Simple Icons repository and usage notes: <https://github.com/simple-icons/simple-icons>
- Simple Icons v16 CDN root: <https://cdn.jsdelivr.net/npm/simple-icons@v16/icons/>

