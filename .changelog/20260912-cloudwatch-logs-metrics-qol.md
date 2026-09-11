* [cloudwatch/web] the stream viewer's Tail toggle now catches up on events logged between the last fetch and the session opening.
  one FilterLogEvents read per opened session, from the newest event on screen up to now, reconciled with the page and the live buffer by count
* [cloudwatch/web] Syntax highlighting applies to collapsed log rows, on their single-line form.
~ [cloudwatch/web] the system map's log peek renders rows through the stream viewer's pipeline instead of as raw text.
  level tint and badge, Format/Syntax/Wrap/Collapse, ANSI colour, a filter over the loaded events, and an "Open in Logs" link to the full view
~ [cloudwatch/web] the CloudWatch Metrics page groups metrics by namespace, adds a filter, and draws the Monitor tab's chart.
  long metric names and dimension values now truncate or wrap inside their column instead of spilling past it; the range presets extend to 7 and 30 days
