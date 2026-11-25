import type { SVGProps } from "react";

/** The inaba mark: a pair of rabbit ears. Drawn like a lucide icon, so it sizes and colors the same way. */
export function Logo(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <ellipse cx="8" cy="10" rx="2.8" ry="7.5" transform="rotate(-10 8 10)" />
      <ellipse cx="16" cy="10" rx="2.8" ry="7.5" transform="rotate(10 16 10)" />
      <path d="M5 21.5a7 4.5 0 0 1 14 0" />
    </svg>
  );
}
