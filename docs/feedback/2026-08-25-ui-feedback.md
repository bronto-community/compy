# UI feedback — 2026-08-25 (verbatim, untriaged, NOTHING actioned yet)

Overall: "most of the UI is not even working, still not looking good, not consistent."

Functional breaks reported:
- Front ○/● activation does not work when clicked.
- Errors still show collector logs (4xx tail-gating not effective in practice).
- del / copy actions not working.
- Collector log search not working.
- Menu bar "Open compy" opens an ADDITIONAL app window every time (no reuse/focus).
- Rollback is orphaned on the menu bar (no context).

Design/UX gripes:
- STATE column redundant (with the dot). SOURCE column gives shipped/local/remote too much importance; the word "shipped" means nothing to users.
- Name rule "lowercase, digits, dashes": why does it exist at all — and if it must, why does the input even accept uppercase?
- Editor: title not editable (rename missing); the properties bar still wastes space.
- Editor variables section "an absolute mess, I don't even know where to start."
- Collector view: stray space before "running"; shows config (bronto) but not the variable set.
- Settings: only shipped-definition collectors are usable; user-added ones can only be removed (unclear what works); "definition" is meaningless terminology.

Process note (self-assessment): headless-screenshot verification cannot catch interaction bugs; several shipped. Future UI work requires interactive click-through verification and design sign-off BEFORE implementation.
