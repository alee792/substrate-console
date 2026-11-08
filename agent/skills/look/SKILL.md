---
name: look
description: See a visible substrate-console change in the demo cluster at desktop and phone widths, in light and dark. Use for any change to the view, the shell or tool output that renders.
---

1. `make build`, then start the demo in the background:
   `bin/substrate-console serve --demo --addr 127.0.0.1:7878 &`
2. Open http://127.0.0.1:7878 and go to what you changed. The demo has
   three OOM crashes on one node, an actor stuck resuming, a nearly full
   pool and a failed golden snapshot, so issue views have real content.
3. If Playwright is available (`node -e "require('playwright')"`), take
   screenshots at 1440x900 and at 390x844, each with
   `colorScheme: 'light'` and `'dark'`, and look at them. Check alignment,
   wrapping, contrast and empty states, not only that it renders.
4. Stop the server when you're done.

Say what you looked at and what you saw. If you couldn't look, say so
rather than describing what it probably looks like.
