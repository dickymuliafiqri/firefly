/** Prompt rendering for the pipe-backed dashboard shell. */

const MARK_START = '\x1e';
const MARK_SEP = '\x1f';

/** Shell prompt state parsed from the server's nonce-tagged status marker. */
export interface PromptState {
  shell: string;
  cwd: string;
  exitCode: number;
}

/** One status marker parsed out of a shell output chunk. */
export interface PromptUpdate {
  cwd: string;
  exitCode: number;
}

/** A compact Linux-style prompt: `cwd (shell) $`, with a failed exit marked. */
export function renderPrompt(state: PromptState): string {
  const color = state.exitCode === 0 ? '\x1b[38;2;190;242;100m' : '\x1b[38;2;244;63;94m';
  const failed = state.exitCode === 0 ? '' : ` [${state.exitCode}]`;
  return `${color}${state.cwd} (${state.shell})${failed} $ \x1b[0m`;
}

/**
 * Strip nonce-tagged status markers from one WebSocket data frame. A marker
 * may be split across frames, so an incomplete tail is carried to the next
 * call. Everything else is plain shell output and is returned unchanged.
 */
export function parsePromptData(chunk: string, nonce: string, carry: string): {
  text: string;
  carry: string;
  updates: PromptUpdate[];
} {
  if (!nonce) return { text: carry + chunk, carry: '', updates: [] };

  const source = carry + chunk;
  const token = MARK_START + nonce;
  const updates: PromptUpdate[] = [];
  let text = '';
  let cursor = 0;

  while (cursor < source.length) {
    const start = source.indexOf(token, cursor);
    if (start < 0) {
      text += source.slice(cursor);
      break;
    }
    text += source.slice(cursor, start);
    const end = source.indexOf(MARK_START, start + token.length);
    if (end < 0) {
      // Frame boundary cut the marker in half; retry with the next chunk.
      return { text, carry: source.slice(start), updates };
    }
    const body = source.slice(start + token.length, end);
    const fields = (body.startsWith(MARK_SEP) ? body.slice(1) : body).split(MARK_SEP);
    const exitCode = Number(fields[0]);
    updates.push({
      cwd: fields.slice(1).join(MARK_SEP),
      exitCode: Number.isFinite(exitCode) ? exitCode : 0,
    });
    cursor = end + MARK_START.length;
    // The probe printed its own newline after the closing marker.
    if (source[cursor] === '\r') cursor += 1;
    if (source[cursor] === '\n') cursor += 1;
  }

  // A chunk may end in the first bytes of the nonce. Keep that fragment so
  // it can be recognised when the rest arrives in the next frame.
  const partial = text.lastIndexOf(MARK_START);
  if (partial >= 0) {
    const tail = text.slice(partial);
    if (token.startsWith(tail)) {
      return { text: text.slice(0, partial), carry: tail, updates };
    }
  }
  return { text, carry: '', updates };
}
