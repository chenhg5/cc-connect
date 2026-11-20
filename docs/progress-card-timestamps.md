# Progress-card occurrence times

Progress cards show `HH:mm:ss ·` before rows with a recorded occurrence time.
Feishu rich and structured cards, Discord structured cards, and card-style
Markdown fallbacks (including Telegram when configured with `progress_style =
"card"`) display these times. Legacy rows without a timestamp keep their previous
appearance.

Each timestamp records when the cc-connect process first observes that row:

- A rich-card tool row is updated in place when its result arrives, retaining the
  tool's start time.
- A structured card appends separate tool-use and tool-result rows. The use row
  shows its start time; the result row shows when the result arrived. For example,
  a tool starting at `14:03:07` and completing thirty seconds later shows
  `14:03:07 ·` on the use row and `14:03:37 ·` on its result row.

Refreshes, truncation, and finalization do not restamp existing rows. Times use
the originating process's local timezone; structured payloads preserve its UTC
offset. No new configuration is required.
