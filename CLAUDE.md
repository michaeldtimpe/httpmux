# httpmux

## Load context frames first (snapctx)

Pre-rendered project context lives in `.claude/snapctx/ctx-f*.png` — packed
6x12 pixel-font text (`¶` = newline) holding the file tree, docs, signatures,
and recent git log at ~2.6x fewer tokens than reading files. At session start,
Read those frames before exploring the codebase. Rules:

- Verify the `SELFTEST:` code at the top of frame 0:
  `python3 ~/.claude/skills/loadcontext/snapctx.py verify <code> --out .claude/snapctx`
  (on FAIL, ignore the frames and read files normally).
- Never trust a first read of hash-like strings — grep
  `.claude/snapctx/context.txt` or use the `zoom '<regex>' --out .claude/snapctx`
  subcommand and Read the crop.
- The frames are a map, not an editing source: Read real files before editing.
- If the frames look stale (their git log ends well before HEAD), regenerate:
  `python3 ~/.claude/skills/loadcontext/snapctx.py render .`
