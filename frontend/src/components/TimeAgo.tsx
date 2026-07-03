import { formatExact, formatRelative } from "../utils/format";

/** Relative/short timestamp with the full local datetime on hover —
 * "3h ago" or "Jun 4", tooltip "June 4, 2026, 2:41 PM EDT". Exists
 * because a bare short date ("6/4/2026") once convinced a user the
 * app was showing dates from the future. Every user-facing timestamp
 * should render through this. */
export default function TimeAgo({
  iso,
  className,
}: {
  iso: string;
  className?: string;
}) {
  return (
    <time dateTime={iso} title={formatExact(iso)} className={className}>
      {formatRelative(iso)}
    </time>
  );
}
