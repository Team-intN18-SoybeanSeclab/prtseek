import { cn } from "@/lib/utils";

export function PrtsMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 48 48" fill="none" aria-hidden="true" className={cn("size-9 shrink-0", className)}>
      <path d="M24 3 45 24 24 45 3 24Z" stroke="currentColor" strokeWidth="1.5" />
      <path d="M16 33V15h10l6 6-6 6H16M27 27l6 6" stroke="currentColor" strokeWidth="2.8" />
      <path d="M3 8h7M3 8v7M38 40h7v-7" stroke="currentColor" strokeWidth="1.5" />
    </svg>
  );
}

export function PrtsBrand({ className, compact = false }: { className?: string; compact?: boolean }) {
  return (
    <span className={cn("prts-brand", className)}>
      <PrtsMark />
      {!compact && (
        <span className="prts-brand-copy">
          <strong>prtseek</strong>
          <span>TACTICAL // CTF CONTROL</span>
        </span>
      )}
    </span>
  );
}
