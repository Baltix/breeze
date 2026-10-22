import { describe, expect, it } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';

/**
 * #9561: the labour rule lives in ONE place per language —
 * `laborHours`/`laborAmount` in packages/shared/src/utils/laborMath.ts and
 * `laborHoursSql`/`laborAmountSql` in apps/api/src/services/currencySql.ts.
 * Before #9561 it was copied into nine files; copies drift (the
 * billableMinutes sweep next door exists because one already did).
 *
 * Two checks:
 *  1. every former copy now routes through a helper (names the helper and no
 *     longer carries the inline formula);
 *  2. no NEW inline copy appears anywhere in the API, web or shared sources.
 */

const REPO = join(__dirname, '../../../..');

const HELPER_FILES = new Set([
  'packages/shared/src/utils/laborMath.ts',
  'apps/api/src/services/currencySql.ts',
]);

/** Former copies (issue #9561 comment) → the helper each must now call. */
const FORMER_COPIES: Record<string, RegExp> = {
  'apps/api/src/services/timeEntryService.ts': /\blaborAmount\(|\blaborAmountSql\(/,
  'apps/api/src/services/invoiceAssembly.ts': /\blaborHours\(/,
  'apps/api/src/services/ticketApproval/gate.ts': /\blaborAmount\(/,
  'apps/api/src/services/orgCurrencyService.ts': /\blaborAmountSql\(/,
  'apps/api/src/services/businessReports/technicianTimeReport.ts': /\blaborAmountSql\(/,
  'apps/api/src/services/contractHourBlocks.ts': /\blaborHundredths\(/,
  'apps/web/src/components/tickets/TicketTimeBilling.tsx': /\blaborHours\(/,
  'apps/web/src/components/time/TimesheetPage.tsx': /\blaborHours\(/,
};

/**
 * Inline shapes of the rule: `(minutes / 60).toFixed(2)`,
 * `Math.round(minutes * 100 / 60)`, and SQL `ROUND(minutes … / 60[.0], 2)`.
 */
const INLINE_FORMULA = [
  /\/\s*60\s*\)\s*\.toFixed\(\s*2\s*\)/,
  /\*\s*100\s*\)?\s*\/\s*60\b/,
  /\/\s*60(?:\.0)?\s*,\s*2\s*\)/i,
];

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    if (entry === 'node_modules' || entry === 'dist' || entry === '.astro') return [];
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) return walk(full);
    return /\.(ts|tsx)$/.test(full) && !/\.test\.tsx?$/.test(full) ? [full] : [];
  });
}

const rel = (file: string) => relative(REPO, file).split('\\').join('/');
const inlineHits = (source: string) =>
  source.split('\n').flatMap((line, i) => (INLINE_FORMULA.some((re) => re.test(line)) ? [i + 1] : []));

describe('labour rule has one implementation (#9561)', () => {
  it.each(Object.entries(FORMER_COPIES))('%s routes through the shared helper', (file, helperCall) => {
    const source = readFileSync(join(REPO, file), 'utf8');
    expect(helperCall.test(source), `${file} must call the labour helper (${helperCall})`).toBe(true);
    expect(inlineHits(source), `${file} still carries an inline copy of the labour formula`).toEqual([]);
  });

  it('no inline copy of the formula exists outside the helpers', () => {
    const offenders: string[] = [];
    for (const root of ['apps/api/src', 'apps/web/src', 'packages/shared/src']) {
      for (const file of walk(join(REPO, root))) {
        const path = rel(file);
        if (HELPER_FILES.has(path)) continue;
        for (const line of inlineHits(readFileSync(file, 'utf8'))) offenders.push(`${path}:${line}`);
      }
    }
    expect(offenders).toEqual([]);
  });
});
